package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/downloader"
	"mye-r/internal/logger"

	"github.com/joho/godotenv"
)

func main() {
	// Parse command line arguments
	itemsFile := flag.String("items", "", "Path to JSON file containing item IDs to process")
	configFile := flag.String("config", "config.yaml", "Path to config file")
	envFile := flag.String("env", ".env", "Path to env file")
	flag.Parse()

	if *itemsFile == "" {
		log.Fatal("No items file specified")
	}

	// Read and parse item IDs
	var itemIDs []int
	file, err := os.Open(*itemsFile)
	if err != nil {
		log.Fatalf("Error opening items file: %v", err)
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(&itemIDs); err != nil {
		log.Fatalf("Error decoding items file: %v", err)
	}

	// Load environment variables
	err = godotenv.Load(*envFile)
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	// Initialize logger
	myLogger := logger.New()
	myLogger.Info("main", "Start", "Starting downloader")

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

	// Initialize downloader
	downloaderManager := downloader.NewRealDebridDownloader(cfg, db)

	// Process each item
	for _, itemID := range itemIDs {
		item, err := db.GetWatchlistItem(itemID)
		if err != nil {
			myLogger.Error("main", "Process", fmt.Sprintf("Error getting item %d: %v", itemID, err))
			continue
		}

		myLogger.Info("main", "Process", fmt.Sprintf("Processing item %d: %s", item.ID, item.Title))

		if err := downloaderManager.Download(item); err != nil {
			myLogger.Error("main", "Process", fmt.Sprintf("Error downloading item %d: %v", item.ID, err))
			continue
		}

		myLogger.Info("main", "Process", fmt.Sprintf("Successfully downloaded item %d", item.ID))
	}
}
