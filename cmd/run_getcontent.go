package main

import (
    "context"
    "flag"
    "fmt"
    "log"
    "os"

    "mye-r/internal/config"
    "mye-r/internal/database"
    "mye-r/internal/getcontent"
    "mye-r/internal/logger"

    "github.com/joho/godotenv"
)

func main() {
    // Parse command line arguments
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
    myLogger.Info("main", "Start", "Starting content fetcher")

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

    // Initialize content fetcher
    contentFetcher, err := getcontent.New(cfg, db)
    if err != nil {
        myLogger.Error("main", "New", fmt.Sprintf("Failed to create content fetcher: %v", err))
        os.Exit(1)
    }

    // Content fetcher is special - it always runs to check for new content
    myLogger.Info("main", "Process", "Checking for new content")
    if err := contentFetcher.Start(context.Background()); err != nil {
        myLogger.Error("main", "Process", fmt.Sprintf("Error fetching content: %v", err))
        os.Exit(1)
    }

    myLogger.Info("main", "Process", "Content fetch completed successfully")
}
