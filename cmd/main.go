package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mye-r/internal"
	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/downloader"
	"mye-r/internal/getcontent"
	"mye-r/internal/indexers"
	"mye-r/internal/librarymatcher"
	"mye-r/internal/logger"
	"mye-r/internal/scraper"
	"mye-r/internal/symlinker"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var customLogger = logger.New()

func main() {
	customLogger.Info("Application", "Start", "Starting application...")
	if err := godotenv.Load(); err != nil {
		customLogger.Warning("Application", "Config", "Warning: .env file not found")
	} else {
		customLogger.Info("Application", "Config", ".env file loaded successfully")
	}

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		customLogger.Error("Application", "Config", "Failed to load configuration: "+err.Error())
		os.Exit(1)
	}
	customLogger.Info("Application", "Config", "Configuration loaded successfully")

	db, err := database.NewDB(cfg.Database.URL)
	if err != nil {
		customLogger.Error("Application", "Database", "Failed to initialize database: "+err.Error())
		os.Exit(1)
	}
	defer db.Close()
	customLogger.Info("Application", "Database", "Database connection established")

	// Initialize the run manager
	runManager := internal.NewRunManager(cfg, db)
	customLogger.Info("Application", "RunManager", "Run manager initialized")

	// Initialize and register all components in order of processing
	if cfg.Fetchers["plexrss"].Enabled {
		customLogger.Info("Application", "ContentFetcher", "Registering content fetcher...")
		contentFetcher := getcontent.New(cfg, db)
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "getcontent",
			Process:    contentFetcher,
		})
	}

	if cfg.TMDB.Enabled {
		customLogger.Info("Application", "TMDBIndexer", "Registering TMDB indexer...")
		tmdbIndexer := indexers.NewTMDBIndexer(cfg, db, customLogger)
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "tmdb_indexer",
			Process:    tmdbIndexer,
		})
	}

	if cfg.Scraping.Scrapers["torrentio"].Enabled {
		customLogger.Info("Application", "Scraper", "Registering scraper...")
		scraperManager := scraper.NewScraperManager(cfg, db)
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "scraper",
			Process:    scraperManager,
		})
	}

	if cfg.Programs.LibraryMatcher.Active {
		customLogger.Info("Application", "LibraryMatcher", "Registering library matcher...")
		libraryMatcherManager := librarymatcher.New(cfg, db)
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "library_matcher",
			Process:    libraryMatcherManager,
		})
	}

	if cfg.Programs.Downloader.Active {
		customLogger.Info("Application", "Downloader", "Registering downloader...")
		downloaderManager := downloader.NewRealDebridDownloader(cfg, db)

		// Fetch the next item for download
		item, err := db.GetNextItemForDownload()
		if err != nil {
			customLogger.Error("Downloader", "GetNextItem", fmt.Sprintf("Error getting next item: %v", err))
		}
		if item != nil {
			err = downloaderManager.Download(item)
			if err != nil {
				customLogger.Error("Downloader", "Download", fmt.Sprintf("Error downloading item: %v", err))
			}
		}
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "downloader",
			Process:    downloaderManager,
		})
	}

	if cfg.Programs.Symlinker.Active {
		customLogger.Info("Application", "Symlinker", "Registering symlinker...")
		symlinkerManager := symlinker.New(cfg, db)
		runManager.RegisterProcess(&internal.ProcessInfo{
			ProcessName: "symlinker",
			Process:    symlinkerManager,
		})
	}

	// Start the run manager
	customLogger.Info("Application", "RunManager", "Starting run manager...")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	if err := runManager.Start(ctx); err != nil {
		customLogger.Error("Application", "RunManager", fmt.Sprintf("Failed to start run manager: %v", err))
		os.Exit(1)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	customLogger.Info("Application", "Signal", "Waiting for interrupt signal...")
	<-sigChan

	// Graceful shutdown
	customLogger.Info("Application", "Shutdown", "Shutting down gracefully...")
	cancel() // Cancel the context to stop all goroutines
	runManager.Stop()
}
