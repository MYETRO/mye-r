package downloader

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type RealDebridDownloader struct {
	config    *config.Config
	db        *database.DB
	log       *logger.Logger
	client    *http.Client
	component string
	cancel    context.CancelFunc
}

func NewRealDebridDownloader(cfg *config.Config, db *database.DB) *RealDebridDownloader {
	return &RealDebridDownloader{
		config:    cfg,
		db:        db,
		log:       logger.New(),
		client:    &http.Client{},
		component: "RealDebridDownloader",
	}
}

func New(cfg *config.Config, db *database.DB) *RealDebridDownloader {
	return NewRealDebridDownloader(cfg, db)
}

func (d *RealDebridDownloader) Download(item *database.WatchlistItem) error {
	d.log.Info(d.component, "Download", fmt.Sprintf("Starting download for item %d: %s", item.ID, item.Title))

	// Check starting conditions for downloader
	if (item.MediaType.String == "movie" || item.MediaType.String == "tv") &&
		(item.CurrentStep.String == "scraping" || item.CurrentStep.String == "scraped" || item.CurrentStep.String == "downloading") {
		// Additional condition for TV shows: all episodes must be scraped
		// Remove or adjust this check based on your struct definition
		// Additional condition for TV shows: all episodes must be scraped
		if item.MediaType.Valid && item.MediaType.String == "tv" {
			// Check if any episodes are not scraped by querying tv_episodes
			query := `
                SELECT COUNT(*) 
                FROM tv_episodes te
                WHERE te.watchlist_item_id = $1
                AND te.scraped = false;
            `

			var count int
			err := d.db.QueryRow(query, item.ID).Scan(&count)
			if err != nil {
				return fmt.Errorf("error checking if all TV episodes are scraped for item %d: %v", item.ID, err)
			}
			if count > 0 {
				return fmt.Errorf("TV show %d has episodes that are not scraped", item.ID)
			}
		}
		// Update initial fields
		item.CurrentStep = sql.NullString{String: "downloading", Valid: true}
	} else {
		return fmt.Errorf("invalid starting conditions for item %d", item.ID)
	}

	// Get all scrape results for this item
	query := `
		SELECT 
			sr.id, 
			sr.watchlist_item_id, 
			sr.info_hash, 
			sr.scraped_filename, 
			sr.scraped_file_size, 
			sr.scraped_resolution, 
			sr.scraped_score, 
			sr.scraped_codec, 
			sr.status_results, 
			sr.created_at, 
			sr.updated_at, 
			sr.debrid_id, 
			sr.debrid_uri, 
			sr.downloaded
		FROM 
			scrape_results sr
		INNER JOIN watchlistitem wi ON wi.id = sr.watchlist_item_id
		WHERE 
			sr.watchlist_item_id = $1
			AND sr.status_results = 'scraped'
			AND (
				NOT EXISTS (
					SELECT 1
					FROM tv_episodes te
					INNER JOIN seasons s ON s.id = te.season_id
					WHERE s.watchlist_item_id = sr.watchlist_item_id
					AND te.scraped = false
				)
				OR wi.media_type = 'movie'
			)
		ORDER BY 
			sr.scraped_score DESC;
    `

	rows, err := d.db.Query(query, item.ID)
	if err != nil {
		d.log.Error(d.component, "Download", fmt.Sprintf("Failed to query scrape results: %v", err))
		return fmt.Errorf("failed to query scrape results: %v", err)
	}
	defer rows.Close()

	var scrapeResults []*database.ScrapeResult
	for rows.Next() {
		var result database.ScrapeResult
		err := rows.Scan(
			&result.ID,
			&result.WatchlistItemID,
			&result.InfoHash,
			&result.ScrapedFilename,
			&result.ScrapedFileSize,
			&result.ScrapedResolution,
			&result.ScrapedScore,
			&result.ScrapedCodec,
			&result.StatusResults,
			&result.CreatedAt,
			&result.UpdatedAt,
			&result.DebridID,
			&result.DebridURI,
			&result.Downloaded,
		)
		if err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to scan scrape result: %v", err))
			return fmt.Errorf("failed to scan scrape result: %v", err)
		}
		scrapeResults = append(scrapeResults, &result)
	}

	if len(scrapeResults) == 0 {
		d.log.Error(d.component, "Download", "No pending scrape results found")
		return fmt.Errorf("no pending scrape results found")
	}

	d.log.Info(d.component, "Download", fmt.Sprintf("Found %d pending scrape results", len(scrapeResults)))

	allSuccess := true
	for _, result := range scrapeResults {
		d.log.Info(d.component, "Download", fmt.Sprintf("Processing scrape result %d: %s", result.ID, result.ScrapedFilename.String))

		// Add torrent to RealDebrid
		torrentID, err := d.addTorrent(result.InfoHash.String)
		if err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to add torrent: %v", err))
			if err := d.updateDownloadStatus(result, "hash_ignored", "Failed to add torrent"); err != nil {
				d.log.Error(d.component, "Download", fmt.Sprintf("Failed to update status: %v", err))
			}
			allSuccess = false
			continue
		}

		// Select files to download
		if err := d.selectFiles(torrentID); err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to select files: %v", err))
			if err := d.updateDownloadStatus(result, "hash_ignored", "Failed to select files"); err != nil {
				d.log.Error(d.component, "Download", fmt.Sprintf("Failed to update status: %v", err))
			}
			allSuccess = false
			continue
		}

		// Get download link
		downloadLink, err := d.getDownloadLink(torrentID, result)
		if err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to get download link: %v", err))
			if err := d.updateDownloadStatus(result, "hash_ignored", "Failed to get download link"); err != nil {
				d.log.Error(d.component, "Download", fmt.Sprintf("Failed to update status: %v", err))
			}
			allSuccess = false
			continue
		}

		// Update status to downloading
		if err := d.updateDownloadStatus(result, "downloading", downloadLink); err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to update status: %v", err))
			allSuccess = false
			continue
		}

		// Start checking download status
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		if err := d.waitForDownload(ctx, torrentID, result); err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to wait for download: %v", err))
			allSuccess = false
			continue
		}

		// Remove torrent from RealDebrid
		if err := d.removeTorrent(torrentID); err != nil {
			d.log.Error(d.component, "Download", fmt.Sprintf("Failed to remove torrent: %v", err))
		}
	}

	if !allSuccess {
		return fmt.Errorf("some downloads failed")
	}

	// Update completion fields after successful download
	for _, result := range scrapeResults {
		result.StatusResults = sql.NullString{String: "downloaded", Valid: true}
		item.CurrentStep = sql.NullString{String: "downloaded", Valid: true}
	}

	d.log.Info(d.component, "Download", "Download process complete.")
	return nil
}
func (d *RealDebridDownloader) addTorrent(infoHash string) (string, error) {
	apiURL := "https://api.real-debrid.com/rest/1.0/torrents/addMagnet"
	d.log.Info(d.component, "addTorrent", "Request URL: api URL")

	// Create the magnet link
	magnetLink := fmt.Sprintf("magnet:?xt=urn:btih:%s", infoHash)

	// Create form data
	data := fmt.Sprintf("magnet=%s", magnetLink)

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.config.DebridAPI))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		d.log.Error(d.component, "addTorrent", fmt.Sprintf("Failed to add torrent to RealDebrid: %v", err))
		return "", fmt.Errorf("failed to add torrent: %v", err)
	}
	defer resp.Body.Close()

	// Read response body for error logging
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		d.log.Error(d.component, "addTorrent", fmt.Sprintf("Failed to read response body: %v", err))
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	// Check response status code
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		d.log.Error(d.component, "addTorrent", fmt.Sprintf("RealDebrid API error: status=%d body=%s", resp.StatusCode, string(body)))
		return "", fmt.Errorf("RealDebrid API error: status=%d body=%s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		d.log.Error(d.component, "addTorrent", fmt.Sprintf("Failed to decode response: %v, body: %s", err, string(body)))
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	d.log.Info(d.component, "addTorrent", fmt.Sprintf("Successfully added torrent to RealDebrid: %s", result.ID))
	return result.ID, nil
}

func (d *RealDebridDownloader) getDownloadLink(torrentID string, scrapeResult *database.ScrapeResult) (string, error) {
	url := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/info/%s", torrentID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.config.DebridAPI))

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	status, ok := result["status"].(string)
	if !ok || status != "downloaded" {
		if status == "queued" || status == "downloading" {
			// Remove the torrent from RealDebrid
			if err := d.removeTorrent(torrentID); err != nil {
				return "", fmt.Errorf("failed to remove torrent: %v", err)
			}

			// Mark this hash as ignored so scraper can find another one
			if err := d.updateDownloadStatus(scrapeResult, "hash_ignored", fmt.Sprintf("Torrent status was: %s", status)); err != nil {
				return "", fmt.Errorf("failed to update scrape result: %v", err)
			}
		}
		return "", fmt.Errorf("torrent not ready for download, status: %s", status)
	}

	links, ok := result["links"].([]interface{})
	if !ok || len(links) == 0 {
		return "", fmt.Errorf("no download links found")
	}

	downloadLink, ok := links[0].(string)
	if !ok {
		return "", fmt.Errorf("failed to get download link from response")
	}

	return downloadLink, nil
}

func (d *RealDebridDownloader) selectFiles(torrentID string) error {
	url := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/selectFiles/%s", torrentID)
	data := "files=all" // Select all files; you can customize this to select specific files
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.config.DebridAPI))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Log the response body for debugging
	body, _ := ioutil.ReadAll(resp.Body)
	d.log.Info(d.component, "selectFiles", fmt.Sprintf("Response Body: %s", string(body)))

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (d *RealDebridDownloader) removeTorrent(torrentID string) error {
	url := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/delete/%s", torrentID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.config.DebridAPI))

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := ioutil.ReadAll(resp.Body)
		d.log.Error(d.component, "removeTorrent", fmt.Sprintf("Response Body: %s", string(body)))
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (d *RealDebridDownloader) updateDownloadStatus(scrapeResult *database.ScrapeResult, status string, details string) error {
	d.log.Info(d.component, "UpdateStatus", fmt.Sprintf("Updating status for scrape result %d: %s -> %s", scrapeResult.ID, scrapeResult.StatusResults.String, status))

	// If we're setting hash_ignored, we need to update the watchlist item status back to scraping_pending
	if status == "hash_ignored" {
		// Get the watchlist item
		item, err := d.db.GetWatchlistItem(int(scrapeResult.WatchlistItemID.Int32))
		if err != nil {
			d.log.Error(d.component, "UpdateStatus", fmt.Sprintf("Failed to get watchlist item: %v", err))
		} else {
			// Update item status to trigger re-scraping
			item.Status = sql.NullString{String: "scrape_failed", Valid: true}
			item.CurrentStep = sql.NullString{String: "scraping_pending", Valid: true}
			if err := d.db.UpdateWatchlistItem(item); err != nil {
				d.log.Error(d.component, "UpdateStatus", fmt.Sprintf("Failed to update item status: %v", err))
			} else {
				d.log.Info(d.component, "UpdateStatus", fmt.Sprintf("Reset item %d status to scraping_pending", item.ID))
			}
		}
	}

	scrapeResult.StatusResults = sql.NullString{
		String: status,
		Valid:  true,
	}
	scrapeResult.UpdatedAt = time.Now()

	if err := d.db.UpdateScrapeResult(scrapeResult); err != nil {
		d.log.Error(d.component, "UpdateStatus", fmt.Sprintf("Failed to update scrape result status: %v", err))
		return fmt.Errorf("failed to update status: %v", err)
	}
	d.log.Info(d.component, "UpdateStatus", fmt.Sprintf("Successfully updated scrape result %d status to %s (%s)", scrapeResult.ID, status, details))
	return nil
}

func (d *RealDebridDownloader) checkDownloadStatus(torrentID string, result *database.ScrapeResult) error {
	url := fmt.Sprintf("https://api.real-debrid.com/rest/1.0/torrents/info/%s", torrentID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.config.DebridAPI))

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get torrent info: %v", err)
	}
	defer resp.Body.Close()

	var torrentInfo struct {
		Status   string   `json:"status"`
		Links    []string `json:"links"`
		Progress float64  `json:"progress"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&torrentInfo); err != nil {
		return fmt.Errorf("failed to decode response: %v", err)
	}

	d.log.Info(d.component, "checkDownloadStatus", fmt.Sprintf("Torrent progress: %.2f%%", torrentInfo.Progress))

	// RealDebrid uses progress 100 to indicate download is complete
	if torrentInfo.Progress >= 100 {
		// Update status to downloaded
		if err := d.updateDownloadStatus(result, "downloaded", ""); err != nil {
			return fmt.Errorf("failed to update status: %v", err)
		}

		// Update the WatchlistItem status as well
		item, err := d.db.GetWatchlistItem(int(result.WatchlistItemID.Int32))
		if err == nil {
			item.Status = sql.NullString{String: "downloaded", Valid: true}
			item.CurrentStep = sql.NullString{String: "completed", Valid: true}
			if err := d.db.UpdateWatchlistItem(item); err != nil {
				d.log.Error(d.component, "checkDownloadStatus", fmt.Sprintf("Failed to update watchlist item status: %v", err))
			}
		}

		// Update the ScrapeResult status as well
		result.StatusResults = sql.NullString{String: "downloaded", Valid: true}
		result.Downloaded = true
		if err := d.db.UpdateScrapeResult(result); err != nil {
			d.log.Error(d.component, "checkDownloadStatus", fmt.Sprintf("Failed to update scrape result status: %v", err))
		}
		return nil
	}

	return fmt.Errorf("download not complete, progress: %.2f%%", torrentInfo.Progress)
}

func (d *RealDebridDownloader) waitForDownload(ctx context.Context, torrentID string, result *database.ScrapeResult) error {
	maxAttempts := 30 // 5 minutes (10 second intervals)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("download cancelled")
		default:
			err := d.checkDownloadStatus(torrentID, result)
			if err == nil {
				return nil
			}
			d.log.Info(d.component, "waitForDownload", fmt.Sprintf("Waiting for download... attempt %d/%d", attempt+1, maxAttempts))
			time.Sleep(10 * time.Second)
		}
	}
	return fmt.Errorf("download did not complete within timeout")
}

func (d *RealDebridDownloader) Start(ctx context.Context) error {
	d.log.Info(d.component, "Start", "Starting downloader")

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				d.log.Info(d.component, "Start", "Context cancelled, stopping downloader")
				return
			case <-ticker.C:
				item, err := d.db.GetNextItemForDownload()
				if err != nil {
					d.log.Error(d.component, "Start", fmt.Sprintf("Error getting next item: %v", err))
					continue
				}
				if item != nil {
					if err := d.Download(item); err != nil {
						d.log.Error(d.component, "Start", fmt.Sprintf("Error downloading item %d: %v", item.ID, err))
					}
				}
			}
		}
	}()
	return nil
}

func (d *RealDebridDownloader) Stop() error {
	d.log.Info(d.component, "Stop", "Stopping downloader")
	if d.cancel != nil {
		d.cancel()
	}
	return nil
}

func (d *RealDebridDownloader) Name() string {
	return "debrid"
}

func (d *RealDebridDownloader) IsNeeded() bool {
	var count int
	err := d.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem 
        WHERE status = 'ready_for_download' 
        AND current_step = 'download_pending'
    `).Scan(&count)

	return err == nil && count > 0
}
