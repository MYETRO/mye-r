package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"mye-r/internal/database"
	"mye-r/internal/logger"

	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	// Initialize logger
	myLogger := logger.New()
	myLogger.Info("main", "Database Connection", "Attempting to connect to the database.")

	// Initialize database connection
	dbURL := os.Getenv("DATABASE_URL")
	db, err := database.NewDB(dbURL)
	if err != nil {
		myLogger.Error("main", "NewDB", fmt.Sprintf("Failed to connect to database: %v", err))
		os.Exit(1)
	}
	defer db.Close()

	// Get items that need TMDB updates
	itemIDs, err := db.GetItemsForTMDB()
	if err != nil {
		myLogger.Error("main", "GetItemsForTMDB", fmt.Sprintf("Failed to get items: %v", err))
		os.Exit(1)
	}

	// Write item IDs to JSON file
	outputFile := "tmdb_items.json"
	file, err := os.Create(outputFile)
	if err != nil {
		myLogger.Error("main", "Create", fmt.Sprintf("Failed to create output file: %v", err))
		os.Exit(1)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(itemIDs); err != nil {
		myLogger.Error("main", "Encode", fmt.Sprintf("Failed to encode items: %v", err))
		os.Exit(1)
	}

	myLogger.Info("main", "Success", fmt.Sprintf("Found %d items needing TMDB updates. Written to %s", len(itemIDs), outputFile))
}
