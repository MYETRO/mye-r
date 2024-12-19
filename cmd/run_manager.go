package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"

	"github.com/joho/godotenv"
)

type ProcessInfo struct {
	Name     string
	ItemIDs  []int
	TempFile string
}

func main() {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	// Initialize logger
	myLogger := logger.New()
	myLogger.Info("main", "Start", "Starting run manager")

	// Load configuration
	cfg, err := config.LoadConfig("config.yaml")
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

	// Create temp directory for item ID files
	tempDir := filepath.Join(os.TempDir(), "mye-r")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		myLogger.Error("main", "MkdirAll", fmt.Sprintf("Failed to create temp directory: %v", err))
		os.Exit(1)
	}

	// Get items to process for each program
	processes := []ProcessInfo{
		{Name: "tmdb_indexer", ItemIDs: getItemsForTMDB(db)},
		{Name: "scraper", ItemIDs: getItemsForScraper(db)},
		{Name: "downloader", ItemIDs: getItemsForDownloader(db)},
		{Name: "librarymatcher", ItemIDs: getItemsForLibraryMatcher(db)},
	}

	// Create temp files and start processes
	for i := range processes {
		if len(processes[i].ItemIDs) > 0 {
			// Create temp file for item IDs
			tempFile := filepath.Join(tempDir, fmt.Sprintf("%s_items.json", processes[i].Name))
			if err := writeItemsToFile(tempFile, processes[i].ItemIDs); err != nil {
				myLogger.Error("main", "WriteItems", fmt.Sprintf("Failed to write items to file for %s: %v", processes[i].Name, err))
				continue
			}
			processes[i].TempFile = tempFile

			// Start the process
			cmd := exec.Command(fmt.Sprintf("run_%s", processes[i].Name))
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			myLogger.Info("main", "StartProcess", fmt.Sprintf("Starting %s with %d items", processes[i].Name, len(processes[i].ItemIDs)))
			if err := cmd.Start(); err != nil {
				myLogger.Error("main", "StartProcess", fmt.Sprintf("Failed to start %s: %v", processes[i].Name, err))
				continue
			}

			// Don't wait for process to finish, let it run in background
			go func(cmd *exec.Cmd, name string, tempFile string) {
				if err := cmd.Wait(); err != nil {
					myLogger.Error("main", "WaitProcess", fmt.Sprintf("%s failed: %v", name, err))
				}
				// Clean up temp file
				if err := os.Remove(tempFile); err != nil {
					myLogger.Error("main", "Cleanup", fmt.Sprintf("Failed to remove temp file for %s: %v", name, err))
				}
			}(cmd, processes[i].Name, tempFile)
		} else {
			myLogger.Info("main", "Process", fmt.Sprintf("No items to process for %s", processes[i].Name))
		}
	}

	// Keep the manager running
	for {
		time.Sleep(30 * time.Second)
		// Check for new items and start new processes if needed
		for _, p := range processes {
			itemIDs := []int{}
			switch p.Name {
			case "tmdb_indexer":
				itemIDs = getItemsForTMDB(db)
			case "scraper":
				itemIDs = getItemsForScraper(db)
			case "downloader":
				itemIDs = getItemsForDownloader(db)
			case "librarymatcher":
				itemIDs = getItemsForLibraryMatcher(db)
			}

			if len(itemIDs) > 0 {
				// Create temp file for item IDs
				tempFile := filepath.Join(tempDir, fmt.Sprintf("%s_items.json", p.Name))
				if err := writeItemsToFile(tempFile, itemIDs); err != nil {
					myLogger.Error("main", "WriteItems", fmt.Sprintf("Failed to write items to file for %s: %v", p.Name, err))
					continue
				}

				// Start the process
				cmd := exec.Command(fmt.Sprintf("run_%s", p.Name))
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				myLogger.Info("main", "StartProcess", fmt.Sprintf("Starting %s with %d items", p.Name, len(itemIDs)))
				if err := cmd.Start(); err != nil {
					myLogger.Error("main", "StartProcess", fmt.Sprintf("Failed to start %s: %v", p.Name, err))
					continue
				}

				// Don't wait for process to finish, let it run in background
				go func(cmd *exec.Cmd, name string, tempFile string) {
					if err := cmd.Wait(); err != nil {
						myLogger.Error("main", "WaitProcess", fmt.Sprintf("%s failed: %v", name, err))
					}
					// Clean up temp file
					if err := os.Remove(tempFile); err != nil {
						myLogger.Error("main", "Cleanup", fmt.Sprintf("Failed to remove temp file for %s: %v", name, err))
					}
				}(cmd, p.Name, tempFile)
			}
		}
	}
}

func writeItemsToFile(filename string, items []int) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(items)
}

func getItemsForTMDB(db *database.DB) []int {
	items, err := db.GetItemsForTMDB()
	if err != nil {
		log.Printf("Error getting items for TMDB: %v", err)
		return nil
	}
	return items
}

func getItemsForScraper(db *database.DB) []int {
	items, err := db.GetItemsForScraper()
	if err != nil {
		log.Printf("Error getting items for scraper: %v", err)
		return nil
	}
	return items
}

func getItemsForDownloader(db *database.DB) []int {
	items, err := db.GetItemsForDownloader()
	if err != nil {
		log.Printf("Error getting items for downloader: %v", err)
		return nil
	}
	return items
}

func getItemsForLibraryMatcher(db *database.DB) []int {
	items, err := db.GetItemsForLibraryMatcher()
	if err != nil {
		log.Printf("Error getting items for library matcher: %v", err)
		return nil
	}
	return items
}
