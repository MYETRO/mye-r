package internal

import (
	"time"
	"database/sql"
	"fmt"

	"mye-r/internal/config"
	"mye-r/internal/database"
)

type BaseProcessor struct {
	name       string
	db         *database.DB
	config     *config.Config
	retryCount map[int]int // map[itemID]retryCount
}

func NewBaseProcessor(name string, db *database.DB, cfg *config.Config) *BaseProcessor {
	return &BaseProcessor{
		name:       name,
		db:         db,
		config:     cfg,
		retryCount: make(map[int]int),
	}
}

func (bp *BaseProcessor) Name() string {
	return bp.name
}

func (bp *BaseProcessor) handleRetry(itemID int) bool {
	maxRetries := bp.getMaxRetries()
	bp.retryCount[itemID]++

	if bp.retryCount[itemID] >= maxRetries {
		// Update status to failed and reset retry count after wait time
		bp.updateStatusFailed(itemID)
		go bp.scheduleRetryReset(itemID)
		return false
	}
	return true
}

func (bp *BaseProcessor) scheduleRetryReset(itemID int) {
	waitTime := bp.getRetryWaitTime()
	time.Sleep(waitTime)
	delete(bp.retryCount, itemID)
	bp.updateStatusNew(itemID)
}

func (bp *BaseProcessor) getMaxRetries() int {
	// Get process-specific max retries from config if available
	if bp.config.ProcessManagement.DefaultMaxRetries > 0 {
		return bp.config.ProcessManagement.DefaultMaxRetries
	}
	return 3 // Default max retries
}

func (bp *BaseProcessor) getRetryWaitTime() time.Duration {
	// Get process-specific retry wait time from config if available
	if bp.config.ProcessManagement.DefaultRetryWaitTime > 0 {
		return bp.config.ProcessManagement.DefaultRetryWaitTime
	}
	return 5 * time.Minute // Default wait time
}

func (bp *BaseProcessor) updateStatusFailed(itemID int) error {
	// Update the item status to failed in the database
	item, err := bp.db.GetWatchlistItemByID(itemID)
	if err != nil {
		return fmt.Errorf("failed to get watchlist item %d: %v", itemID, err)
	}

	if item == nil {
		return fmt.Errorf("watchlist item %d not found", itemID)
	}

	item.Status = sql.NullString{String: "failed", Valid: true}
	return bp.db.UpdateWatchlistItem(item)
}

func (bp *BaseProcessor) updateStatusNew(itemID int) error {
	// Reset the item status to new in the database
	item, err := bp.db.GetWatchlistItemByID(itemID)
	if err != nil {
		return fmt.Errorf("failed to get watchlist item %d: %v", itemID, err)
	}

	if item == nil {
		return fmt.Errorf("watchlist item %d not found", itemID)
	}

	item.Status = sql.NullString{String: "new", Valid: true}
	return bp.db.UpdateWatchlistItem(item)
}
