package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "os"

    "mye-r/internal/config"
    "mye-r/internal/database"
    "mye-r/internal/librarymatcher"
    "mye-r/internal/logger"

    "github.com/joho/godotenv"
)

func main() {
    // Parse command line arguments
    itemsFile := flag.String("items", "", "Path to JSON file containing item IDs to process")
    configFile := flag.String("config", "config.yaml", "Path to config file")
    envFile := flag.String("env", ".env", "Path to env file")
    itemID := flag.Int("id", 0, "Single item ID to process")
    flag.Parse()

    var itemIDs []int

    // Check if a single item ID was provided
    if *itemID > 0 {
        itemIDs = []int{*itemID}
    } else if *itemsFile != "" {
        // Read and parse item IDs from file
        file, err := os.Open(*itemsFile)
        if err != nil {
            log.Fatalf("Error opening items file: %v", err)
        }
        defer file.Close()

        if err := json.NewDecoder(file).Decode(&itemIDs); err != nil {
            log.Fatalf("Error decoding items file: %v", err)
        }
    } else {
        // Check if a non-flag argument was provided (for backward compatibility)
        args := flag.Args()
        if len(args) > 0 {
            var id int
            _, err := fmt.Sscanf(args[0], "%d", &id)
            if err == nil && id > 0 {
                itemIDs = []int{id}
            }
        }
        
        if len(itemIDs) == 0 {
            log.Fatal("No item ID or items file specified. Use -id <number> or -items <file>")
        }
    }

    // Load environment variables
    err := godotenv.Load(*envFile)
    if err != nil {
        log.Println("Warning: .env file not found")
    }

    // Initialize logger
    myLogger := logger.New()
    myLogger.Info("main", "Start", "Starting library matcher")

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

    // Initialize library matcher
    matcher := librarymatcher.New(cfg, db)

    // Process each item
    for _, itemID := range itemIDs {
        item, err := db.GetWatchlistItem(itemID)
        if err != nil {
            myLogger.Error("main", "Process", fmt.Sprintf("Error getting item %d: %v", itemID, err))
            continue
        }

        myLogger.Info("main", "Process", fmt.Sprintf("Processing item %d: %s", item.ID, item.Title))

        if err := matcher.Match(item); err != nil {
            myLogger.Error("main", "Process", fmt.Sprintf("Error matching item %d: %v", item.ID, err))
            continue
        }

        myLogger.Info("main", "Process", fmt.Sprintf("Successfully matched item %d", item.ID))
    }
}
