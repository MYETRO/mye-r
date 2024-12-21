package main

import (
    "context"
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

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

    // Create context that we can cancel on shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Initialize content fetcher
    contentFetcher, err := getcontent.New(cfg, db)
    if err != nil {
        myLogger.Error("main", "New", fmt.Sprintf("Failed to create content fetcher: %v", err))
        os.Exit(1)
    }

    // Start the content fetcher
    myLogger.Info("main", "Process", "Starting content fetcher")
    if err := contentFetcher.Start(ctx); err != nil {
        myLogger.Error("main", "Process", fmt.Sprintf("Error starting content fetcher: %v", err))
        os.Exit(1)
    }

    // Set up signal handling
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    // Wait for shutdown signal
    sig := <-sigChan
    myLogger.Info("main", "Shutdown", fmt.Sprintf("Received signal %v, shutting down", sig))

    // Cancel context to signal shutdown
    cancel()

    // Clean shutdown
    if err := contentFetcher.Stop(); err != nil {
        myLogger.Error("main", "Shutdown", fmt.Sprintf("Error during shutdown: %v", err))
    }

    myLogger.Info("main", "Shutdown", "Content fetcher stopped successfully")
}
