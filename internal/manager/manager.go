package manager

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"mye-r/internal/database"
	"mye-r/internal/indexers"
	"mye-r/internal/scraper"
)

type Manager struct {
	db      *database.DB
	indexer *indexers.TMDBIndexer
	scraper *scraper.Scraper
	cron    *cron.Cron
}

func New(db *database.DB, indexer *indexers.TMDBIndexer, scraper *scraper.Scraper) *Manager {
	return &Manager{
		db:      db,
		indexer: indexer,
		scraper: scraper,
		cron:    cron.New(),
	}
}

func (m *Manager) Start() error {
	// Schedule the check for new episodes at 6 PM daily
	_, err := m.cron.AddFunc("0 18 * * *", m.checkForNewEpisodes)
	if err != nil {
		return fmt.Errorf("error scheduling new episodes check: %v", err)
	}

	m.cron.Start()
	return nil
}

func (m *Manager) Stop() {
	if m.cron != nil {
		m.cron.Stop()
	}
}

func (m *Manager) checkForNewEpisodes() {
	items, err := m.db.GetReturningSeriesWithUnscrapedEpisodes()
	if err != nil {
		log.Printf("Error getting returning series: %v", err)
		return
	}

	for _, item := range items {
		// Reset the status to trigger re-indexing
		item.Status = sql.NullString{String: "new", Valid: true}
		item.CurrentStep = sql.NullString{String: "indexing_pending", Valid: true}
		item.LastScrapedDate = sql.NullTime{Time: time.Now(), Valid: true}

		err = m.db.UpdateWatchlistItem(item)
		if err != nil {
			log.Printf("Error updating watchlist item %d: %v", item.ID, err)
			continue
		}

		log.Printf("Found new episodes for series: %s", item.Title)
	}
}
