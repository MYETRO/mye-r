package database

import (
	"database/sql"
	"fmt"
	"time"
)

type ScrapeResult struct {
	ID                int            `json:"id"`
	WatchlistItemID   sql.NullInt32  `json:"watchlist_item_id"`
	ScrapedFilename   sql.NullString `json:"scraped_filename"`
	ScrapedResolution sql.NullString `json:"scraped_resolution"`
	ScrapedDate       sql.NullTime   `json:"scraped_date"`
	InfoHash          sql.NullString `json:"info_hash"`
	ScrapedScore      sql.NullInt32  `json:"scraped_score"`
	ScrapedFileSize   sql.NullString `json:"scraped_file_size"`
	ScrapedCodec      sql.NullString `json:"scraped_codec"`
	StatusResults     sql.NullString `json:"status_results"`
	DebridID          sql.NullString `json:"debrid_id"`
	DebridURI         sql.NullString `json:"debrid_uri"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Downloaded        bool           `json:"downloaded"`
}

// GetNextScrapeResultForDownload gets the next pending scrape result
func (db *DB) GetNextScrapeResultForDownload() (*ScrapeResult, error) {
	query := `
		SELECT id, watchlist_item_id, scraped_filename, scraped_resolution,
			   scraped_date, info_hash, scraped_score, scraped_file_size,
			   scraped_codec, status_results, debrid_id, debrid_uri,
			   created_at, updated_at, downloaded
		FROM scrape_results
		WHERE status_results = 'scraped'
		ORDER BY scraped_score DESC
		LIMIT 1
	`
	var result ScrapeResult
	err := db.QueryRow(query).Scan(
		&result.ID, &result.WatchlistItemID, &result.ScrapedFilename,
		&result.ScrapedResolution, &result.ScrapedDate, &result.InfoHash,
		&result.ScrapedScore, &result.ScrapedFileSize, &result.ScrapedCodec,
		&result.StatusResults, &result.DebridID, &result.DebridURI,
		&result.CreatedAt, &result.UpdatedAt, &result.Downloaded,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get next scrape result for download: %v", err)
	}
	return &result, nil
}

// UpdateScrapeResult updates an existing scrape result
func (db *DB) UpdateScrapeResult(result *ScrapeResult) error {
	query := `
		UPDATE scrape_results
		SET scraped_filename = $2, scraped_resolution = $3, scraped_date = $4,
			info_hash = $5, scraped_score = $6, scraped_file_size = $7,
			scraped_codec = $8, status_results = $9, debrid_id = $10,
			debrid_uri = $11, downloaded = $12, updated_at = $13
		WHERE id = $1
	`
	_, err := db.Exec(query,
		result.ID, result.ScrapedFilename, result.ScrapedResolution,
		result.ScrapedDate, result.InfoHash, result.ScrapedScore,
		result.ScrapedFileSize, result.ScrapedCodec, result.StatusResults,
		result.DebridID, result.DebridURI, result.Downloaded, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to update scrape result: %v", err)
	}
	return nil
}

// GetLatestScrapeResult gets the most recent scrape result for an item
func (db *DB) GetLatestScrapeResult(itemID int) (*ScrapeResult, error) {
	query := `
		SELECT id, watchlist_item_id, scraped_filename, scraped_resolution,
			   scraped_date, info_hash, scraped_score, scraped_file_size,
			   scraped_codec, status_results, debrid_id, debrid_uri,
			   created_at, updated_at, downloaded
		FROM scrape_results
		WHERE watchlist_item_id = $1
		ORDER BY scraped_date DESC
		LIMIT 1
	`
	var result ScrapeResult
	err := db.QueryRow(query, itemID).Scan(
		&result.ID, &result.WatchlistItemID, &result.ScrapedFilename,
		&result.ScrapedResolution, &result.ScrapedDate, &result.InfoHash,
		&result.ScrapedScore, &result.ScrapedFileSize, &result.ScrapedCodec,
		&result.StatusResults, &result.DebridID, &result.DebridURI,
		&result.CreatedAt, &result.UpdatedAt, &result.Downloaded,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get latest scrape result: %v", err)
	}
	return &result, nil
}

func (db *DB) GetExistingHashForItem(itemID int) (string, error) {
	query := `
		SELECT info_hash
		FROM scrape_results
		WHERE watchlist_item_id = $1 AND info_hash IS NOT NULL
		LIMIT 1
	`
	var infoHash sql.NullString
	err := db.QueryRow(query, itemID).Scan(&infoHash)
	if err == sql.ErrNoRows {
		return "", nil // No existing hash
	}
	if err != nil {
		return "", fmt.Errorf("failed to get existing hash for item: %v", err)
	}
	return infoHash.String, nil
}

func (db *DB) UpdateScrapeResultStatus(itemID int, status string) error {
	query := `
		UPDATE scrape_results
		SET status_results = $2, updated_at = $3
		WHERE watchlist_item_id = $1
	`
	_, err := db.Exec(query, itemID, status, time.Now())
	if err != nil {
		return fmt.Errorf("failed to update scrape result status: %v", err)
	}
	return nil
}