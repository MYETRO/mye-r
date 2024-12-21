package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/indexers"
	"mye-r/internal/logger"
	"mye-r/internal/scraper"

	"bufio"
	"os/exec"

	"github.com/robfig/cron/v3"
)

type Process interface {
	Start(ctx context.Context) error
	Stop() error
	IsNeeded() bool
	Name() string
}

type DBInterface interface {
	GetNextItemForSymlinking() (*database.WatchlistItem, error)
	UpdateWatchlistItem(*database.WatchlistItem) error
	GetLatestScrapeResult(int) (*database.ScrapeResult, error)
	GetScrapeResultsForItem(int) ([]*database.ScrapeResult, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Exec(query string, args ...interface{}) (sql.Result, error)
}

type RunManager struct {
	processes map[string]*ProcessInfo
	db        *database.DB
	log       *logger.Logger
	ctx       context.Context
	mutex     sync.Mutex
	cfg       *config.Config
	binaries  map[string]string
	cron      *cron.Cron
	indexer   *indexers.TMDBIndexer
	scraper   *scraper.ScraperManager
}

func NewRunManager(cfg *config.Config, db *database.DB, indexer *indexers.TMDBIndexer, scraper *scraper.ScraperManager) *RunManager {
	return &RunManager{
		processes: make(map[string]*ProcessInfo),
		db:        db,
		log:       logger.New(),
		cfg:       cfg,
		binaries:  make(map[string]string),
		cron:      cron.New(),
		indexer:   indexer,
		scraper:   scraper,
	}
}

func (rm *RunManager) RegisterProcess(p *ProcessInfo) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	rm.processes[p.ProcessName] = p
	rm.log.Info("RunManager", "RegisterProcess", "Registered process")
}

func (rm *RunManager) Start(ctx context.Context) error {
	rm.ctx = ctx
	rm.log.Info("RunManager", "Start", "Starting run manager")

	// Schedule the check for new episodes at 6 PM daily
	_, err := rm.cron.AddFunc("0 18 * * *", rm.checkForNewEpisodes)
	if err != nil {
		return fmt.Errorf("error scheduling new episodes check: %v", err)
	}
	rm.cron.Start()

	// Build all binaries at startup
	if err := rm.buildBinaries(); err != nil {
		return fmt.Errorf("failed to build binaries: %v", err)
	}

	// Start content fetcher immediately as it needs to run continuously
	binPath, exists := rm.binaries["getcontent"]
	if !exists {
		return fmt.Errorf("getcontent binary not found")
	}

	// Get config file path
	cwd, err := os.Getwd()
	if err != nil {
		rm.log.Error("RunManager", "Start", "Failed to get working directory")
		return fmt.Errorf("failed to get working directory: %v", err)
	}
	configPath := filepath.Join(cwd, "config.yaml")
	envPath := filepath.Join(cwd, ".env")

	// Start getcontent process
	cmd := exec.Command(binPath,
		"--config", configPath,
		"--env", envPath)
	cmd.Dir = filepath.Dir(binPath)
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		rm.log.Error("RunManager", "Start", "Failed to start content fetcher")
		return fmt.Errorf("failed to start content fetcher: %v", err)
	}

	// Run content fetcher in background
	go func() {
		if err := cmd.Wait(); err != nil {
			rm.log.Error("RunManager", "Start", "Process exited with error")
		}
	}()

	rm.log.Info("RunManager", "Start", "Started content fetcher")

	// Initial queue status check
	rm.logQueueStatus()

	// Start the main processing loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				rm.checkAndRunProcesses()
				time.Sleep(5 * time.Second)
			}
		}
	}()

	return nil
}

func (rm *RunManager) logQueueStatus() {
	itemsByProcess := rm.getAllItemsToProcess()

	// Only log status if there are items to process
	hasItems := false
	for _, items := range itemsByProcess {
		if len(items) > 0 {
			hasItems = true
			break
		}
	}

	if hasItems {
		rm.log.Info("RunManager", "logQueueStatus", "=== Current Processing Queue ===")
		for _, name := range []string{"tmdb_indexer", "librarymatcher", "scraper", "downloader", "symlinker"} {
			if items, exists := itemsByProcess[name]; exists {
				rm.log.Info("RunManager", "logQueueStatus", fmt.Sprintf("%s: %d items pending", name, len(items)))
			}
		}
		rm.log.Info("RunManager", "logQueueStatus", "===============================")
	}
}

func (rm *RunManager) buildBinaries() error {
	cwd, err := os.Getwd()
	if err != nil {
		rm.log.Error("RunManager", "buildBinaries", "Failed to get working directory")
		return fmt.Errorf("failed to get working directory: %v", err)
	}

	processOrder := []string{
		"getcontent",
		"tmdb_indexer",
		"librarymatcher",
		"scraper",
		"downloader",
		"symlinker",
	}

	// Map of process names to their binary names
	binaryNames := map[string]string{
		"getcontent":     "getcontent",
		"tmdb_indexer":   "tmdb_indexer",
		"librarymatcher": "librarymatcher",
		"scraper":        "scraper",
		"downloader":     "downloader",
		"symlinker":      "symlinker",
	}

	// Look for binaries in the bin directory
	binDir := filepath.Join(cwd, "bin")
	for _, name := range processOrder {
		binName := binaryNames[name]
		binPath := filepath.Join(binDir, binName)
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}

		// Check if binary exists
		if _, err := os.Stat(binPath); err != nil {
			rm.log.Error("RunManager", "buildBinaries", fmt.Sprintf("Binary not found at path %s: %v", binPath, err))
			continue // Skip this binary but continue with others
		}

		rm.binaries[name] = binPath
		rm.log.Info("RunManager", "buildBinaries", fmt.Sprintf("Found binary for %s at %s", name, binPath))
	}

	if len(rm.binaries) == 0 {
		return fmt.Errorf("no binaries found in %s", binDir)
	}

	return nil
}

func (rm *RunManager) checkAndRunProcesses() {
	itemsByProcess := rm.getAllItemsToProcess()

	processOrder := []string{
		"tmdb_indexer",
		"librarymatcher",
		"scraper",
		"downloader",
		"symlinker",
	}

	// Get working directory once
	cwd, err := os.Getwd()
	if err != nil {
		rm.log.Error("RunManager", "checkAndRunProcesses", "Failed to get working directory")
		return
	}

	// Get config file path
	configPath := filepath.Join(cwd, "config.yaml")
	envPath := filepath.Join(cwd, ".env")

	// Create a wait group for parallel processing
	var wg sync.WaitGroup

	// Start each process in parallel
	for _, name := range processOrder {
		if items, exists := itemsByProcess[name]; exists && len(items) > 0 {
			if !rm.isProcessEnabled(name) {
				rm.log.Debug("RunManager", "checkAndRunProcesses", "Process is disabled")
				continue
			}

			// Process items in smaller batches
			batchSize := 10
			if name == "librarymatcher" {
				batchSize = 20 // Library matcher can handle more items
			}

			// Start a goroutine for this process
			wg.Add(1)
			go func(processName string, processItems []*database.WatchlistItem) {
				defer wg.Done()

				for i := 0; i < len(processItems); i += batchSize {
					end := i + batchSize
					if end > len(processItems) {
						end = len(processItems)
					}
					batch := processItems[i:end]

					rm.log.Info("RunManager", "checkAndRunProcesses", fmt.Sprintf("Starting processor %s", processName))

					// Create a temporary file with the item IDs
					tempFile, err := os.CreateTemp("", "items_*.json")
					if err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", "Failed to create temp file")
						continue
					}
					defer os.Remove(tempFile.Name())

					// Write item IDs to temp file
					itemIDs := make([]int, len(batch))
					for j, item := range batch {
						itemIDs[j] = item.ID
						rm.log.Info("RunManager", "checkAndRunProcesses", fmt.Sprintf("Processing item %d in %s", item.ID, processName))
					}

					if err := json.NewEncoder(tempFile).Encode(itemIDs); err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", "Failed to write to temp file")
						continue
					}
					tempFile.Close()

					// Run the pre-built binary
					binPath, exists := rm.binaries[processName]
					if !exists {
						rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Binary not found for process %s", processName))
						continue
					}

					rm.log.Info("RunManager", "checkAndRunProcesses", fmt.Sprintf("Starting process %s with %d items", processName, len(batch)))

					// Create command with working directory set to binary location
					cmd := exec.Command(binPath,
						"--items", tempFile.Name(),
						"--config", configPath,
						"--env", envPath)
					cmd.Dir = filepath.Dir(binPath)
					cmd.Env = os.Environ()

					// Set up pipes for stdout and stderr
					stdout, err := cmd.StdoutPipe()
					if err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Failed to create stdout pipe for %s: %v", processName, err))
						continue
					}
					stderr, err := cmd.StderrPipe()
					if err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Failed to create stderr pipe for %s: %v", processName, err))
						continue
					}

					// Start the command
					if err := cmd.Start(); err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Failed to start process %s: %v", processName, err))
						continue
					}

					// Read output asynchronously
					go func() {
						scanner := bufio.NewScanner(stdout)
						for scanner.Scan() {
							rm.log.Info("RunManager", processName, scanner.Text())
						}
					}()

					go func() {
						scanner := bufio.NewScanner(stderr)
						for scanner.Scan() {
							rm.log.Error("RunManager", processName, scanner.Text())
						}
					}()

					// Wait for the command to finish
					if err := cmd.Wait(); err != nil {
						rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Process %s failed: %v", processName, err))
						continue
					}

					rm.log.Info("RunManager", "checkAndRunProcesses", fmt.Sprintf("Completed batch for %s", processName))

					// Log updated queue status after each batch
					rm.logQueueStatus()

					// Small delay between batches to prevent resource exhaustion
					time.Sleep(500 * time.Millisecond)
				}
			}(name, items)
		}
	}

	// Wait for all processes to complete
	wg.Wait()
}

func (rm *RunManager) getAllItemsToProcess() map[string][]*database.WatchlistItem {
	items := make(map[string][]*database.WatchlistItem)

	// Get items for TMDB indexer (status = 'new', current_step = 'indexing_pending')
	indexingItems, err := rm.db.GetItemsByStatusAndStep("new", "indexing", "", "")
	if err != nil {
		rm.log.Error("RunManager", "getAllItemsToProcess", "Failed to get items for indexing")
	} else if len(indexingItems) > 0 {
		items["tmdb_indexer"] = indexingItems
		rm.log.Debug("RunManager", "getAllItemsToProcess", "Found items for indexing")
	}

	// Get items for library matcher (current_step = 'indexed', current_step = 'library_matching', scrape_results.status_results = library_matched)
	libraryMatchItems, err := rm.db.GetItemsByStatusAndStep("indexed", "library_matching", "library_matched", "")
	if err != nil {
		rm.log.Error("RunManager", "getAllItemsToProcess", "Failed to get items for library matching")
	} else if len(libraryMatchItems) > 0 {
		items["librarymatcher"] = libraryMatchItems
		rm.log.Debug("RunManager", "getAllItemsToProcess", "Found items for library matching")
	}

	// Get items for scraper (status = 'library_matched', current_step = 'scraping_pending')
	scrapingItems, err := rm.db.GetItemsByStatusAndStep("library_matched", "scraping", "scraped", "hash_ignored")
	if err != nil {
		rm.log.Error("RunManager", "getAllItemsToProcess", "Failed to get items for scraping")
	} else if len(scrapingItems) > 0 {
		items["scraper"] = scrapingItems
		rm.log.Debug("RunManager", "getAllItemsToProcess", "Found items for scraping")
	}

	// Get items for downloader (status = 'ready_for_download', current_step = 'download_pending')
	downloadItems, err := rm.db.GetItemsByStatusAndStep("scraping", "scraped", "downloading", "scraped")
	if err != nil {
		rm.log.Error("RunManager", "getAllItemsToProcess", "Failed to get items for download")
	} else if len(downloadItems) > 0 {
		// Filter out items without scrape results to prevent downloader errors
		var validDownloadItems []*database.WatchlistItem
		for _, item := range downloadItems {
			if result, err := rm.db.GetLatestScrapeResult(item.ID); err == nil && result != nil {
				validDownloadItems = append(validDownloadItems, item)
			} else {
				rm.log.Debug("RunManager", "getAllItemsToProcess", fmt.Sprintf("Skipping download for item %d: no valid scrape result", item.ID))
				// Update item status to indicate missing scrape result
				item.Status = sql.NullString{String: "scrape_failed", Valid: true}
				item.CurrentStep = sql.NullString{String: "scraping_pending", Valid: true}
				if err := rm.db.UpdateWatchlistItem(item); err != nil {
					rm.log.Error("RunManager", "getAllItemsToProcess", fmt.Sprintf("Failed to update item status: %v", err))
				}
			}
		}
		if len(validDownloadItems) > 0 {
			items["downloader"] = validDownloadItems
			rm.log.Debug("RunManager", "getAllItemsToProcess", fmt.Sprintf("Found %d items with valid scrape results for downloading", len(validDownloadItems)))
		}
	}

	// Get items for symlinker (status = 'downloaded', current_step = 'symlink_pending')
	symlinkItems, err := rm.db.GetItemsByStatusAndStep("downloading", "downloaded", "symlinking", "downloaded")
	if err != nil {
		rm.log.Error("RunManager", "getAllItemsToProcess", "Failed to get items for symlinking")
	} else if len(symlinkItems) > 0 {
		items["symlinker"] = symlinkItems
		// Log the items being sent to the symlinker
		for _, item := range symlinkItems {
			rm.log.Debug("RunManager", "getAllItemsToProcess", fmt.Sprintf("Item for symlinking: ID: %d, Title: %s", item.ID, item.Title))
		}
		rm.log.Debug("RunManager", "getAllItemsToProcess", "Found items for symlinking")
	}

	return items
}

func (rm *RunManager) Stop() {
	if rm.cron != nil {
		rm.cron.Stop()
	}
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	// Create a context with timeout for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a wait group to track all stopping processes
	var wg sync.WaitGroup
	for name, proc := range rm.processes {
		wg.Add(1)
		go func(name string, proc *ProcessInfo) {
			defer wg.Done()
			rm.stopProcess(name, proc)
		}(name, proc)
	}

	// Wait for all processes to stop or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		rm.log.Info("RunManager", "Stop", "All processes stopped gracefully")
	case <-ctx.Done():
		rm.log.Error("RunManager", "Stop", "Timeout waiting for processes to stop")
	}
}

func (rm *RunManager) stopProcess(name string, proc *ProcessInfo) {
	rm.log.Info("RunManager", "stopProcess", fmt.Sprintf("Stopping process: %s", name))

	if err := proc.Stop(); err != nil {
		rm.log.Error("RunManager", "stopProcess", fmt.Sprintf("Error stopping process %s: %v", name, err))
	}
}

func (rm *RunManager) isProcessEnabled(name string) bool {
	switch name {
	case "getcontent":
		return rm.cfg.Programs.ContentFetcher.Active
	case "tmdb_indexer":
		return rm.cfg.TMDB.Enabled
	case "scraper":
		return rm.cfg.Programs.Scraper.Active
	case "downloader":
		return rm.cfg.Programs.Downloader.Active
	case "librarymatcher":
		return rm.cfg.Programs.LibraryMatcher.Active
	case "symlinker":
		return rm.cfg.Programs.Symlinker.Active
	default:
		return false
	}
}

type ProcessInfo struct {
	ProcessName string  // Name of the process
	Process     Process // The actual process implementation
}

func (p *ProcessInfo) Start(ctx context.Context) error {
	return p.Process.Start(ctx)
}

func (p *ProcessInfo) Stop() error {
	return p.Process.Stop()
}

func (p *ProcessInfo) IsNeeded() bool {
	return p.Process.IsNeeded()
}

func (p *ProcessInfo) Name() string {
	return p.ProcessName
}

func (rm *RunManager) checkForNewEpisodes() {
	items, err := rm.db.GetReturningSeriesWithUnscrapedEpisodes()
	if err != nil {
		rm.log.Error("RunManager", "checkForNewEpisodes", "Failed to get returning series")
		return
	}

	for _, item := range items {
		// Get episodes that need processing (released but not symlinked)
		episodes, err := rm.db.GetUnprocessedEpisodes(item.ID)
		if err != nil {
			rm.log.Error("RunManager", "checkForNewEpisodes", "Failed to get unprocessed episodes")
			continue
		}

		for _, episode := range episodes {
			if !episode.Scraped {
				rm.log.Info("RunManager", "checkForNewEpisodes", "Found unscraped episode")

				// Update episode status to trigger scraping
				episode.Scraped = false
				if err := rm.db.UpdateTVEpisode(episode); err != nil {
					rm.log.Error("RunManager", "checkForNewEpisodes", "Failed to update episode status")
				}
				continue
			}

			// Check scrape results status
			results, err := rm.db.GetScrapeResultsByEpisode(episode.ID)
			if err != nil {
				rm.log.Error("RunManager", "checkForNewEpisodes", "Failed to get scrape results")
				continue
			}

			for _, result := range results {
				if !result.StatusResults.Valid {
					continue
				}

				switch result.StatusResults.String {
				case "scraped":
					rm.log.Info("RunManager", "checkForNewEpisodes", "Episode ready for download")
				case "downloaded":
					rm.log.Info("RunManager", "checkForNewEpisodes", "Episode ready for symlinking")
				}
			}
		}
	}
}
