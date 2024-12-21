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
	cancel   context.CancelFunc
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

func (sm *ScraperManager) Start(ctx context.Context) error {
	sm.log.Info("ScraperManager", "Start", "Starting scraper manager")

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	sm.cancel = cancel

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				sm.log.Info("ScraperManager", "Start", "Context cancelled, stopping scraper manager")
				return
			case <-ticker.C:
				sm.RunScrapers(ctx)
			}
		}
	}()
	return nil
}

func (sm *ScraperManager) Stop() error {
	sm.log.Info("ScraperManager", "Stop", "Stopping scraper manager")
	if sm.cancel != nil {
		sm.cancel()
	}
	return nil
}

func (sm *ScraperManager) RunScrapers(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
		sm.log.Debug("ScraperManager", "RunScrapers", "Fetching next item for scraping")
		item, err := sm.db.GetNextItemForScraping()
		if err != nil {
			sm.log.Error("ScraperManager", "RunScrapers", "Failed to get next item for scraping")
			return
		}

		if item == nil {
			sm.log.Debug("ScraperManager", "RunScrapers", "No items to scrape")
			return
		}

		sm.log.Info("ScraperManager", "RunScrapers", "Processing new item")

		for _, scraper := range sm.scrapers {
			select {
			case <-ctx.Done():
				return
			default:
				scraperConfig := sm.config.Scraping.Scrapers[scraper.Name()]

				// Check if the scraper is restricted to specific custom libraries
				if len(scraperConfig.OnlyForCustomLibrary) > 0 && !utils.Contains(scraperConfig.OnlyForCustomLibrary, item.CustomLibrary.String) {
					continue
				}

				err := scraper.Scrape(item)
				if err != nil {
					sm.log.Error("ScraperManager", "RunScrapers", "Scraping failed")
					continue
				}

				sm.log.Info("ScraperManager", "RunScrapers", "Scraping completed successfully")
				break // Stop after first successful scrape
			}
		}

		// Update item status
		result, err := sm.db.GetLatestScrapeResult(item.ID)
		if err == nil && result != nil && result.ScrapedFilename.Valid && result.ScrapedFilename.String != "" {
			// Check if the scrape result status is "scraped" or "pending _download"
			if result.StatusResults.String == "scraped" || result.StatusResults.String == "scraping" {
				// Update the CurrentStep to "scraped" if the scrape was successful
				item.CurrentStep = sql.NullString{String: "scraped", Valid: true}

				// Update the item in the database
				if err = sm.db.UpdateWatchlistItem(item); err != nil {
					sm.log.Error("ScraperManager", "RunScrapers", "Failed to update item status")
				} else {
					sm.log.Debug("ScraperManager", "RunScrapers", "Status updated to 'scraped'")
				}
			}
		} else {
			sm.log.Error("ScraperManager", "RunScrapers", "No valid scrape result found for item")
		}
	}
}

func (sm *ScraperManager) Name() string {
	return "torrentio"
}

func (sm *ScraperManager) IsNeeded() bool {
	var count int
	err := sm.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem wi
        LEFT JOIN scrape_results sr ON sr.watchlist_item_id = wi.id
        WHERE (wi.current_step = 'library_matched' 
               OR wi.current_step = 'scraping')
          OR sr.status_results = 'hash_ignored'
    `).Scan(&count)

	return err == nil && count > 0
}

func (sm *ScraperManager) ScrapeSingle(itemID int) error {
	item, err := sm.db.GetWatchlistItem(itemID)
	if err != nil {
		sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to get watchlist item %d: %v", itemID, err))
		return fmt.Errorf("failed to get item: %w", err)
	}

	if item.CurrentStep.String != "library_matched" && item.CurrentStep.String != "scraping" {
		return fmt.Errorf("item not ready for scraping (CurrentStep not library_matched or scraping): %s", item.CurrentStep.String)
	}

	// Get existing scrape results
	existingResults, err := sm.db.GetScrapeResultsForItem(itemID)
	if err != nil {
		sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to get scrape results for item %d: %v", itemID, err))
		return fmt.Errorf("failed to get existing scrape results: %w", err)
	}

	// Check if we need to find more results
	needsMoreResults := true
	if len(existingResults) > 0 {
		needsMoreResults = false
		var failedStates []string
		for _, result := range existingResults {
			// If any result is in these states, we need more results
			switch result.StatusResults.String {
			case "scraping_failed", "hash_ignored", "download_failed":
				needsMoreResults = true
				failedStates = append(failedStates, result.StatusResults.String)
			}
		}

		if !needsMoreResults {
			// Show the best existing result
			var bestResult *database.ScrapeResult
			bestScore := int32(-1)
			for _, result := range existingResults {
				if result.ScrapedScore.Valid && result.ScrapedScore.Int32 > bestScore {
					bestResult = result
					bestScore = result.ScrapedScore.Int32
				}
			}
			if bestResult != nil {
				sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Using existing scrape result with score %d", bestScore))

				// Update item status if it hasn't been updated yet
				if item.CurrentStep.String != "scraped" {
					item.CurrentStep = sql.NullString{String: "scraped", Valid: true}
					if err = sm.db.UpdateWatchlistItem(item); err != nil {
						sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to update item status: %v", err))
					} else {
						sm.log.Debug("ScraperManager", "ScrapeSingle", "Status updated to scraped")
					}
				} else {
					sm.log.Debug("ScraperManager", "ScrapeSingle", "Status already up to date")
				}
			} else {
				sm.log.Info("ScraperManager", "ScrapeSingle", "No valid results found in existing scrape results")
			}
			return nil
		} else if len(failedStates) > 0 {
			sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Retrying scrape due to previous failures: %v", failedStates))
		}
	}

	for _, scraper := range sm.scrapers {
		scraperConfig := sm.config.Scraping.Scrapers[scraper.Name()]

		// Check if the scraper is restricted to specific custom libraries
		if len(scraperConfig.OnlyForCustomLibrary) > 0 && !utils.Contains(scraperConfig.OnlyForCustomLibrary, item.CustomLibrary.String) {
			sm.log.Debug("ScraperManager", "ScrapeSingle", fmt.Sprintf("Skipping scraper %s - not configured for library %s", scraper.Name(), item.CustomLibrary.String))
			continue
		}

		sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Attempting scrape with %s", scraper.Name()))
		err := scraper.Scrape(item)
		if err != nil {
			sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Scraping failed with %s: %v", scraper.Name(), err))
			continue
		}

		sm.log.Info("ScraperManager", "ScrapeSingle", fmt.Sprintf("Scraping completed successfully with %s", scraper.Name()))

		// Update scrape results status to 'scraped'
		result, err := sm.db.GetLatestScrapeResult(item.ID)
		if err == nil && result != nil {
			result.StatusResults = sql.NullString{String: "scraped", Valid: true}
			if err = sm.db.UpdateScrapeResult(result); err != nil {
				sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to update scrape result status: %v", err))
			} else {
				sm.log.Debug("ScraperManager", "ScrapeSingle", "Scrape result status updated to 'scraped'")
			}
		} else {
			sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to get latest scrape result: %v", err))
		}

		// Update item status
		item.CurrentStep = sql.NullString{String: "scraped", Valid: true}
		if err = sm.db.UpdateWatchlistItem(item); err != nil {
			sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to update item status: %v", err))
		} else {
			sm.log.Debug("ScraperManager", "ScrapeSingle", "Status updated to 'scraped'")
		}

		// Update each episode as scraped
		episodes, err := sm.db.GetTVEpisodesForItem(itemID)
		if err != nil {
			sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to get episodes for item %d: %v", itemID, err))
		} else {
			for _, episode := range episodes {
				episode.Scraped = true
				if err := sm.db.UpdateTVEpisode(&episode); err != nil {
					sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("Failed to update episode %d as scraped: %v", episode.ID, err))
				} else {
					sm.log.Debug("ScraperManager", "ScrapeSingle", fmt.Sprintf("Episode %d updated as scraped", episode.ID))
				}
			}
		}

		return nil
	}

	sm.log.Error("ScraperManager", "ScrapeSingle", fmt.Sprintf("All scrapers failed for item %d", itemID))
	return fmt.Errorf("scraping failed with all available scrapers")
}
