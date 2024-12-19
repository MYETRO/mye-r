package scraper

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
	"mye-r/internal/utils"
)

type Scraper interface {
	Scrape(item *database.WatchlistItem) error
	Name() string
}

type ScraperManager struct {
	config   *config.Config
	db       *database.DB
	log      *logger.Logger
	scrapers []Scraper
}

func NewScraperManager(cfg *config.Config, db *database.DB) *ScraperManager {
	log := logger.New()
	manager := &ScraperManager{
		config: cfg,
		db:     db,
		log:    log,
	}

	// Initialize scrapers
	for scraperName, scraperConfig := range cfg.Scraping.Scrapers {
		if scraperConfig.Enabled {
			switch scraperName {
			case "torrentio":
				manager.scrapers = append(manager.scrapers, NewTorrentioScraper(cfg, db, scraperName, scraperConfig))
			// Add cases for other scrapers as they are implemented
			default:
				log.Warning("ScraperManager", "NewScraperManager", fmt.Sprintf("Unknown scraper type: %s", scraperName))
			}
		}
	}

	// Sort scrapers by priority
	sort.Slice(manager.scrapers, func(i, j int) bool {
		return manager.config.Scraping.Scrapers[manager.scrapers[i].Name()].Priority <
			manager.config.Scraping.Scrapers[manager.scrapers[j].Name()].Priority
	})

	return manager
}

func (sm *ScraperManager) RunScrapers(ctx context.Context) {
	sm.log.Info("ScraperManager", "RunScrapers", "Starting scraper manager")
	for {
		select {
		case <-ctx.Done():
			sm.log.Info("ScraperManager", "RunScrapers", "Scraper manager shutting down")
			return
		default:
			sm.log.Debug("ScraperManager", "RunScrapers", "Fetching next item for scraping")
			item, err := sm.db.GetNextItemForScraping()
			if err != nil {
				sm.log.Error("ScraperManager", "RunScrapers", fmt.Sprintf("Error getting next item for scraping: %v", err))
				time.Sleep(5 * time.Second)
				continue
			}

			if item == nil {
				sm.log.Debug("ScraperManager", "RunScrapers", "No items to scrape, waiting...")
				time.Sleep(5 * time.Minute)
				continue
			} else {
				sm.log.Debug("ScraperManager", "RunScrapers", fmt.Sprintf("Found item to scrape: %s (ID: %d)", item.Title, item.ID))
			}

			sm.log.Info("ScraperManager", "RunScrapers", fmt.Sprintf("Scraping item: %s", item.Title))

			for _, scraper := range sm.scrapers {
				scraperConfig := sm.config.Scraping.Scrapers[scraper.Name()]

				// Check if the scraper is restricted to specific custom libraries
				if len(scraperConfig.OnlyForCustomLibrary) > 0 && !utils.Contains(scraperConfig.OnlyForCustomLibrary, item.CustomLibrary.String) {
					continue
				}

				err := scraper.Scrape(item)
				if err != nil {
					sm.log.Error("ScraperManager", "RunScrapers", fmt.Sprintf("Error scraping item %d with %s: %v", item.ID, scraper.Name(), err))
					continue
				}

				sm.log.Info("ScraperManager", "RunScrapers", fmt.Sprintf("Successfully scraped item %d with %s", item.ID, scraper.Name()))
				break // Stop after first successful scrape
			}

			// Update item status
			result, err := sm.db.GetLatestScrapeResult(item.ID)
			if err == nil && result != nil && result.ScrapedFilename.Valid && result.ScrapedFilename.String != "" {
				item.Status = sql.NullString{String: "ready_for_download", Valid: true}
				item.CurrentStep = sql.NullString{String: "download_pending", Valid: true}
			} else {
				item.Status = sql.NullString{String: "scrape_failed", Valid: true}
			}
			if err = sm.db.UpdateWatchlistItem(item); err != nil {
				sm.log.Error("ScraperManager", "RunScrapers", fmt.Sprintf("Error updating item status: %v", err))
			} else {
				sm.log.Debug("ScraperManager", "RunScrapers", fmt.Sprintf("Successfully updated item status: %s", item.Status.String))
			}

			// Implement rate limiting if configured
			if sm.config.Scraping.Scrapers["torrentio"].Ratelimit {
				time.Sleep(1 * time.Second)
			}
		}
	}
}

func (sm *ScraperManager) Start(ctx context.Context) error {
	sm.log.Info("ScraperManager", "Start", "Starting scraper manager")
	go sm.RunScrapers(ctx)
	return nil
}

func (sm *ScraperManager) Stop() error {
	sm.log.Info("ScraperManager", "Stop", "Stopping scraper manager")
	return nil
}

func (sm *ScraperManager) Name() string {
	return "torrentio"
}

func (sm *ScraperManager) IsNeeded() bool {
	var count int
	err := sm.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem 
        WHERE status = 'new' 
        AND current_step = 'scrape_pending'
    `).Scan(&count)

	return err == nil && count > 0
}

func (sm *ScraperManager) ScrapeSingle(itemID int) error {
	item, err := sm.db.GetWatchlistItem(itemID)
	if err != nil {
		return fmt.Errorf("failed to get item: %v", err)
	}

	// Get existing scrape results
	existingResults, err := sm.db.GetScrapeResultsForItem(itemID)
	if err != nil {
		return fmt.Errorf("failed to get existing scrape results: %v", err)
	}

	// Check if we need to find more results
	needsMoreResults := true
	if len(existingResults) > 0 {
		needsMoreResults = false
		for _, result := range existingResults {
			// If any result is in these states, we need more results
			switch result.StatusResults.String {
			case "scraping_failed", "downloader_ignored_hash", "download_failed":
				needsMoreResults = true
			}
		}
	}

	if !needsMoreResults {
		sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Item %d already has valid results", itemID))
		return nil
	}

	for _, scraper := range sm.scrapers {
		scraperConfig := sm.config.Scraping.Scrapers[scraper.Name()]

		// Check if the scraper is restricted to specific custom libraries
		if len(scraperConfig.OnlyForCustomLibrary) > 0 && !utils.Contains(scraperConfig.OnlyForCustomLibrary, item.CustomLibrary.String) {
			continue
		}

		err := scraper.Scrape(item)
		if err != nil {
			sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Error scraping item %d with %s: %v", item.ID, scraper.Name(), err))
			continue
		}

		sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Successfully scraped item %d with %s", item.ID, scraper.Name()))
		return nil
	}

	return fmt.Errorf("failed to scrape item with any available scraper")
}
