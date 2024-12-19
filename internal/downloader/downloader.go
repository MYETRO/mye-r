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
	config *config.Config
	db     *database.DB
	log    *logger.Logger
	client *http.Client
}

func NewRealDebridDownloader(cfg *config.Config, db *database.DB) *RealDebridDownloader {
	return &RealDebridDownloader{
		config: cfg,
		db:     db,
		log:    logger.New(),
		client: &http.Client{},
	}
}

func New(cfg *config.Config, db *database.DB) *RealDebridDownloader {
	return NewRealDebridDownloader(cfg, db)
}

func (d *RealDebridDownloader) Download(item *database.WatchlistItem) error {
	// Get all scrape results for this item
	scrapeResults, err := d.db.GetScrapeResultsForItem(item.ID)
	if err != nil {
		return fmt.Errorf("failed to get scrape results: %v", err)
	}

	if len(scrapeResults) == 0 {
		return fmt.Errorf("no scrape results found for item %d", item.ID)
	}

	// For TV shows, we need to download each episode
	if item.MediaType.Valid && item.MediaType.String == "tv" {
		for _, result := range scrapeResults {
			if result.StatusResults.String == "scraped" {
				d.log.Info("RealDebridDownloader", "Download", fmt.Sprintf("Starting download for %s - %s",
					item.Title, result.ScrapedFilename.String))

				// Add torrent to RealDebrid
				torrentID, err := d.addTorrent(result.InfoHash.String)
				if err != nil {
					d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to add torrent: %v", err))
					// Mark this hash as ignored so scraper can find another one
					if err := d.updateDownloadStatus(&result, "downloader_ignored_hash", err.Error()); err != nil {
						d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to update status: %v", err))
					}
					continue
				}

				// Select files to download
				if err := d.selectFiles(torrentID); err != nil {
					d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to select files: %v", err))
					// Mark this hash as ignored
					if err := d.updateDownloadStatus(&result, "downloader_ignored_hash", "Failed to select files"); err != nil {
						d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to update status: %v", err))
					}
					continue
				}

				// Get download link
				downloadLink, err := d.getDownloadLink(torrentID, &result)
				if err != nil {
					d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to get download link: %v", err))
					// Mark this hash as ignored
					if err := d.updateDownloadStatus(&result, "downloader_ignored_hash", "Failed to get download link"); err != nil {
						d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to update status: %v", err))
					}
					continue
				}

				// Update status to downloading
				if err := d.updateDownloadStatus(&result, "downloading", downloadLink); err != nil {
					d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to update status: %v", err))
					continue
				}

				// Wait for download to complete and update status
				if err := d.waitForDownload(torrentID, &result); err != nil {
					d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to wait for download: %v", err))
					if err := d.updateDownloadStatus(&result, "download_failed", err.Error()); err != nil {
						d.log.Error("RealDebridDownloader", "Download", fmt.Sprintf("Failed to update status: %v", err))
					}
					continue
				}
			}
		}
		return nil
	}

	// For movies, find the best quality version that hasn't been ignored
	var bestResult *database.ScrapeResult
	bestScore := int32(0)
	for i := range scrapeResults {
		result := &scrapeResults[i]
		if result.StatusResults.String == "scraped" && 
		   result.ScrapedScore.Valid && 
		   result.ScrapedScore.Int32 > bestScore {
			bestScore = result.ScrapedScore.Int32
			bestResult = result
		}
	}

	if bestScore == 0 {
		return fmt.Errorf("no valid scrape results found for item %d", item.ID)
	}

	d.log.Info("RealDebridDownloader", "Download", fmt.Sprintf("Starting download for %s (InfoHash: %s)",
		item.Title, bestResult.InfoHash.String))

	// Add torrent to RealDebrid
	torrentID, err := d.addTorrent(bestResult.InfoHash.String)
	if err != nil {
		return fmt.Errorf("failed to add torrent: %v", err)
	}

	// Select files to download
	if err := d.selectFiles(torrentID); err != nil {
		return fmt.Errorf("failed to select files: %v", err)
	}

	// Get download link
	downloadLink, err := d.getDownloadLink(torrentID, bestResult)
	if err != nil {
		return fmt.Errorf("failed to get download link: %v", err)
	}

	// Update status to downloading
	if err := d.updateDownloadStatus(bestResult, "downloading", downloadLink); err != nil {
		return fmt.Errorf("failed to update status: %v", err)
	}

	// Wait for download to complete and update status
	if err := d.waitForDownload(torrentID, bestResult); err != nil {
		return fmt.Errorf("failed to wait for download: %v", err)
	}

	// Update status to downloaded
	item.Status = sql.NullString{String: "downloaded", Valid: true}
	item.CurrentStep = sql.NullString{String: "symlink_pending", Valid: true}
	if err := d.db.UpdateWatchlistItem(item); err != nil {
		return fmt.Errorf("failed to update item status: %v", err)
	}

	return nil
}

func (d *RealDebridDownloader) addTorrent(infoHash string) (string, error) {
	apiURL := "https://api.real-debrid.com/rest/1.0/torrents/addMagnet"
	d.log.Info("RealDebridDownloader", "addTorrent", fmt.Sprintf("Request URL: %s", apiURL))

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
		return "", fmt.Errorf("failed to add torrent: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

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
		if status == "queued" {
			// Remove the torrent from RealDebrid
			if err := d.removeTorrent(torrentID); err != nil {
				return "", fmt.Errorf("failed to remove torrent: %v", err)
			}
		}

		// Update the database to indicate a re-scrape is needed
		scrapeResult.StatusResults = sql.NullString{String: "re-scrape", Valid: true}
		if err := d.db.UpdateScrapeResult(scrapeResult); err != nil {
			return "", fmt.Errorf("failed to update scrape result for re-scrape: %v", err)
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
	d.log.Info("RealDebridDownloader", "selectFiles", fmt.Sprintf("Response Body: %s", string(body)))

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
		d.log.Error("RealDebridDownloader", "removeTorrent", fmt.Sprintf("Response Body: %s", string(body)))
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (d *RealDebridDownloader) updateDownloadStatus(scrapeResult *database.ScrapeResult, status string, details string) error {
	scrapeResult.StatusResults = sql.NullString{
		String: status,
		Valid:  true,
	}
	scrapeResult.UpdatedAt = time.Now()

	if err := d.db.UpdateScrapeResult(scrapeResult); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	d.log.Info("RealDebridDownloader", "Status",
		fmt.Sprintf("ID %d: %s - %s", scrapeResult.ID, status, details))
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

	d.log.Info("RealDebridDownloader", "checkDownloadStatus", fmt.Sprintf("Torrent progress: %.2f%%", torrentInfo.Progress))

	// RealDebrid uses progress 100 to indicate download is complete
	if torrentInfo.Progress >= 100 {
		// Update status to downloaded
		if err := d.updateDownloadStatus(result, "downloaded", ""); err != nil {
			return fmt.Errorf("failed to update status: %v", err)
		}
		return nil
	}

	return fmt.Errorf("download not complete, progress: %.2f%%", torrentInfo.Progress)
}

func (d *RealDebridDownloader) waitForDownload(torrentID string, result *database.ScrapeResult) error {
	maxAttempts := 30 // 5 minutes (10 second intervals)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := d.checkDownloadStatus(torrentID, result)
		if err == nil {
			return nil
		}
		d.log.Info("RealDebridDownloader", "waitForDownload", fmt.Sprintf("Waiting for download... attempt %d/%d", attempt+1, maxAttempts))
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("download did not complete within timeout")
}

func (d *RealDebridDownloader) Start(ctx context.Context) error {
	d.log.Info("RealDebridDownloader", "Start", "Starting downloader")
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				item, err := d.db.GetNextItemForDownload()
				if err != nil {
					d.log.Error("RealDebridDownloader", "Start", fmt.Sprintf("Error getting next item: %v", err))
					time.Sleep(5 * time.Second)
					continue
				}
				if item != nil {
					if err := d.Download(item); err != nil {
						d.log.Error("RealDebridDownloader", "Start", fmt.Sprintf("Error downloading item %d: %v", item.ID, err))
					}
				}
				time.Sleep(5 * time.Second)
			}
		}
	}()
	return nil
}

func (d *RealDebridDownloader) Stop() error {
	d.log.Info("RealDebridDownloader", "Stop", "Stopping downloader")
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
        WHERE status = 'new' 
        AND current_step = 'download_pending'
    `).Scan(&count)

	return err == nil && count > 0
}
