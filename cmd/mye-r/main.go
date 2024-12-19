package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"mye-r/internal/database"
	"mye-r/internal/indexer"
	"mye-r/internal/manager"
	"mye-r/internal/scraper"
)

func main() {
	// Initialize database
	db, err := database.New()
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize components
	idx := indexer.New(db)
	scr := scraper.New(db)
	mgr := manager.New(db, idx, scr)

	// Start the manager
	if err := mgr.Start(); err != nil {
		log.Fatalf("Failed to start manager: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	// Cleanup
	mgr.Stop()
}
