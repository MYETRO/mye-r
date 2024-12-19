package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/indexers"
	"mye-r/internal/logger"

	"github.com/joho/godotenv"
)

func main() {
	// Parse command line arguments
	itemsFile := flag.String("items", "", "Path to JSON file containing item IDs to process (optional)")
	configFile := flag.String("config", "config.yaml", "Path to config file")
	envFile := flag.String("env", ".env", "Path to env file")
	flag.Parse()

	// Load environment variables
	err := godotenv.Load(*envFile)
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	// Initialize logger
	myLogger := logger.New()
	myLogger.Info("main", "Database Connection", "Attempting to connect to the database.")

	// Load configuration
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		myLogger.Error("main", "LoadConfig", fmt.Sprintf("Failed to load config: %v", err))
		os.Exit(1)
	}

	// Initialize database connection
	dbURL := os.Getenv("DATABASE_URL")
	db, err := database.NewDB(dbURL)
	if err != nil {
		myLogger.Error("main", "NewDB", fmt.Sprintf("Failed to connect to database: %v", err))
		os.Exit(1)
	}
	defer db.Close()

	// Get items to process
	var itemIDs []int
	if *itemsFile != "" {
		// Read from specified file
		file, err := os.Open(*itemsFile)
		if err != nil {
			myLogger.Error("main", "Open", fmt.Sprintf("Error opening items file: %v", err))
			os.Exit(1)
		}
		defer file.Close()

		if err := json.NewDecoder(file).Decode(&itemIDs); err != nil {
			myLogger.Error("main", "Decode", fmt.Sprintf("Error decoding items file: %v", err))
			os.Exit(1)
		}
	} else {
		// Get items that need TMDB updates from database
		itemIDs, err = db.GetItemsForTMDB()
		if err != nil {
			myLogger.Error("main", "GetItemsForTMDB", fmt.Sprintf("Failed to get items: %v", err))
			os.Exit(1)
		}
	}

	if len(itemIDs) == 0 {
		myLogger.Info("main", "Process", "No items found that need TMDB updates")
		return
	}

	myLogger.Info("main", "Process", fmt.Sprintf("Found %d items to process", len(itemIDs)))

	// Initialize TMDB indexer
	tmdbIndexer := indexers.NewTMDBIndexer(cfg, db, myLogger)

	// Process each item
	for _, itemID := range itemIDs {
		item, err := db.GetWatchlistItem(itemID)
		if err != nil {
			if err == sql.ErrNoRows {
				myLogger.Warning("main", "Process", fmt.Sprintf("Item %d not found", itemID))
				continue
			}
			myLogger.Error("main", "Process", fmt.Sprintf("Error getting item %d: %v", itemID, err))
			continue
		}

		myLogger.Info("main", "Process", fmt.Sprintf("Processing item %d: %s", itemID, item.Title))

		updatedItem, err := tmdbIndexer.UpdateItemWithTMDBData(item)
		if err != nil {
			myLogger.Error("main", "Process", fmt.Sprintf("Error processing item %d: %v", itemID, err))
			continue
		}

		myLogger.Info("main", "Process", fmt.Sprintf("Successfully processed item %d: %s", updatedItem.ID, updatedItem.Title))
	}
}
