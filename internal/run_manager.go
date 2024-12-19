package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"

	"os/exec"
)

type Process interface {
	Start(ctx context.Context) error
	Stop() error
	IsNeeded() bool
	Name() string
}

type RunManager struct {
	processes map[string]*ProcessInfo
	db        *database.DB
	log       *logger.Logger
	ctx       context.Context
	mutex     sync.Mutex
	cfg       *config.Config
	binaries  map[string]string // Cache for compiled binaries
}

func NewRunManager(cfg *config.Config, db *database.DB) *RunManager {
	return &RunManager{
		processes: make(map[string]*ProcessInfo),
		db:        db,
		log:       logger.New(),
		cfg:       cfg,
		binaries:  make(map[string]string),
	}
}

func (rm *RunManager) RegisterProcess(p *ProcessInfo) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	rm.processes[p.ProcessName] = p
	rm.log.Info("RunManager", "RegisterProcess", fmt.Sprintf("Registered process: %s", p.ProcessName))
}

func (rm *RunManager) Start(ctx context.Context) error {
	rm.ctx = ctx
	rm.log.Info("RunManager", "Start", "Starting RunManager")

	// Build all binaries at startup
	if err := rm.buildBinaries(); err != nil {
		return fmt.Errorf("failed to build binaries: %v", err)
	}

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
		rm.log.Info("RunManager", "Status", "=== Current Processing Queue ===")
		for _, name := range []string{"getcontent", "tmdb_indexer", "librarymatcher", "scraper", "downloader", "symlinker"} {
			if items, exists := itemsByProcess[name]; exists {
				if len(items) > 0 {
					rm.log.Info("RunManager", "Status", fmt.Sprintf("%s: %d items pending", name, len(items)))
				}
			}
		}
		rm.log.Info("RunManager", "Status", "===============================")
	}
}

func (rm *RunManager) buildBinaries() error {
	cwd, err := os.Getwd()
	if err != nil {
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

	for _, name := range processOrder {
		srcPath := filepath.Join(cwd, "cmd", fmt.Sprintf("run_%s.go", name))
		binPath := filepath.Join(cwd, "bin", name)
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}

		// Create bin directory if it doesn't exist
		if err := os.MkdirAll(filepath.Join(cwd, "bin"), 0755); err != nil {
			return fmt.Errorf("failed to create bin directory: %v", err)
		}

		// Build the binary
		cmd := exec.Command("go", "build", "-o", binPath, srcPath)
		cmd.Dir = cwd
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to build %s: %v\nOutput: %s", name, err, string(output))
		}

		rm.binaries[name] = binPath
		rm.log.Info("RunManager", "Build", fmt.Sprintf("Built binary for %s", name))
	}

	return nil
}

func (rm *RunManager) checkAndRunProcesses() {
	itemsByProcess := rm.getAllItemsToProcess()
    
    processOrder := []string{
        "getcontent",
        "tmdb_indexer",
        "librarymatcher",
        "scraper",
        "downloader",
        "symlinker",
    }

    // Get working directory once
    cwd, err := os.Getwd()
    if err != nil {
        rm.log.Error("RunManager", "checkAndRunProcesses", fmt.Sprintf("Failed to get working directory: %v", err))
        return
    }

    // Get config file path
    configPath := filepath.Join(cwd, "config.yaml")
    envPath := filepath.Join(cwd, ".env")

    for _, name := range processOrder {
        if items, exists := itemsByProcess[name]; exists && len(items) > 0 {
            if !rm.isProcessEnabled(name) {
                rm.log.Debug("RunManager", name, fmt.Sprintf("Process is disabled, skipping %d items", len(items)))
                continue
            }

            // Process items in smaller batches
            batchSize := 10
            if name == "librarymatcher" {
                batchSize = 20 // Library matcher can handle more items
            }

            for i := 0; i < len(items); i += batchSize {
                end := i + batchSize
                if end > len(items) {
                    end = len(items)
                }
                batch := items[i:end]

                rm.log.Info("RunManager", name, fmt.Sprintf("Starting %s processor for batch %d-%d of %d items", 
                    name, i+1, end, len(items)))

                // Create a temporary file with the item IDs
                tempFile, err := os.CreateTemp("", "items_*.json")
                if err != nil {
                    rm.log.Error("RunManager", name, fmt.Sprintf("Failed to create temp file: %v", err))
                    continue
                }
                defer os.Remove(tempFile.Name())

                // Write item IDs to temp file
                itemIDs := make([]int, len(batch))
                for j, item := range batch {
                    itemIDs[j] = item.ID
                    rm.log.Info("RunManager", name, fmt.Sprintf("Processing item %d: %s", item.ID, item.Title))
                }

                if err := json.NewEncoder(tempFile).Encode(itemIDs); err != nil {
                    rm.log.Error("RunManager", name, fmt.Sprintf("Failed to write to temp file: %v", err))
                    continue
                }
                tempFile.Close()

                // Run the pre-built binary
                binPath, exists := rm.binaries[name]
                if !exists {
                    rm.log.Error("RunManager", name, "Binary not found")
                    continue
                }

                cmd := exec.Command(binPath, 
                    "--items", tempFile.Name(),
                    "--config", configPath,
                    "--env", envPath)
                cmd.Dir = filepath.Dir(binPath)
                cmd.Env = os.Environ()

                output, err := cmd.CombinedOutput()
                if err != nil {
                    rm.log.Error("RunManager", name, fmt.Sprintf("Process failed for items: %v", itemIDs))
                    rm.log.Error("RunManager", name, fmt.Sprintf("Error: %v", err))
                    if len(output) > 0 {
                        rm.log.Error("RunManager", name, fmt.Sprintf("Output: %s", string(output)))
                    }
                    continue
                }

                if len(output) > 0 {
                    rm.log.Debug("RunManager", name, fmt.Sprintf("Process output:\n%s", string(output)))
                }
                rm.log.Info("RunManager", name, fmt.Sprintf("Completed processing batch of %d items", len(batch)))

                // Log updated queue status after each batch
                rm.logQueueStatus()

                // Small delay between batches to prevent resource exhaustion
                time.Sleep(500 * time.Millisecond)
            }
        }
    }
}

func (rm *RunManager) getAllItemsToProcess() map[string][]*database.WatchlistItem {
	items := make(map[string][]*database.WatchlistItem)

	// Get items for content fetcher
	newItems, err := rm.db.GetItemsByStatus("new")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get new items: %v", err))
	} else if len(newItems) > 0 {
		items["getcontent"] = newItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d new items for content fetcher", len(newItems)))
	}

	// Get items for TMDB indexer
	indexingItems, err := rm.db.GetItemsByStatus("indexing_pending")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get items for indexing: %v", err))
	} else if len(indexingItems) > 0 {
		items["tmdb_indexer"] = indexingItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d items pending indexing", len(indexingItems)))
	}

	// Get items for library matcher
	libraryMatchItems, err := rm.db.GetItemsByStatus("librarymatch_pending")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get items for library matching: %v", err))
	} else if len(libraryMatchItems) > 0 {
		items["librarymatcher"] = libraryMatchItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d items pending library matching", len(libraryMatchItems)))
	}

	// Get items for scraper
	scrapingItems, err := rm.db.GetItemsByStatus("scraping_pending")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get items for scraping: %v", err))
	} else if len(scrapingItems) > 0 {
		items["scraper"] = scrapingItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d items pending scraping", len(scrapingItems)))
	}

	// Get items for downloader
	downloadItems, err := rm.db.GetItemsByStatus("download_pending")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get items for download: %v", err))
	} else if len(downloadItems) > 0 {
		items["downloader"] = downloadItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d items pending download", len(downloadItems)))
	}

	// Get items for symlinker
	symlinkItems, err := rm.db.GetItemsByStatus("symlink_pending")
	if err != nil {
		rm.log.Error("RunManager", "GetItems", fmt.Sprintf("Failed to get items for symlinking: %v", err))
	} else if len(symlinkItems) > 0 {
		items["symlinker"] = symlinkItems
		rm.log.Debug("RunManager", "GetItems", fmt.Sprintf("Found %d items pending symlinking", len(symlinkItems)))
	}

	return items
}

func (rm *RunManager) Stop() {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()

	for name, proc := range rm.processes {
		rm.stopProcess(name, proc)
	}
}

func (rm *RunManager) stopProcess(name string, proc *ProcessInfo) {
	rm.log.Info("RunManager", "stopProcess", "Stopping process: "+name)

	if err := proc.Stop(); err != nil {
		log.Printf("Error stopping process %s: %v", name, err)
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

// ProcessInfo implements the Process interface for simple process management
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
