package symlinker

import (
	"context"
	"database/sql"
	"fmt"
	"mye-r/internal/config"
	"mye-r/internal/database"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// DBInterface defines the database operations needed by the symlinker
type DBInterface interface {
	GetNextItemForSymlinking() (*database.WatchlistItem, error)
	GetScrapeResultsForItem(itemID int) ([]*database.ScrapeResult, error)
	UpdateItemStatus(itemID int64, status string, currentStep string) error
	GetItemsWithSymlinks(limit int, offset int) ([]*database.WatchlistItem, error)
	GetItemsByStatusAndStep(currentStep1, currentStep2, currentStep3, statusResults string) ([]*database.WatchlistItem, error) // Updated method
	QueryRow(query string, args ...interface{}) *sql.Row
	Exec(query string, args ...interface{}) (sql.Result, error)
}

type Symlinker struct {
	config *config.Config
	db     DBInterface
	log    *logrus.Logger
}

func New(cfg *config.Config, db DBInterface) *Symlinker {
	return NewSymlinker(cfg, db)
}

func NewSymlinker(cfg *config.Config, db DBInterface) *Symlinker {
	return &Symlinker{
		config: cfg,
		db:     db,
		log:    logrus.New(),
	}
}

func (s *Symlinker) Name() string {
	return "symlinker"
}

// checkSymlinksLastRun tracks when we last ran the symlink check
var checkSymlinksLastRun time.Time

func (s *Symlinker) Start(ctx context.Context) error {
	s.log.Info("Symlinker started")

	// Check symlinks if repair is enabled and interval has passed
	if s.config.Programs.Symlinker.Repair != nil && s.config.Programs.Symlinker.Repair.Enabled {
		if time.Since(checkSymlinksLastRun) > s.config.Programs.Symlinker.Repair.CheckInterval {
			go s.checkSymlinksInBackground(ctx)
		}
	}

	// Start processing loop
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				s.log.Info("Symlinker stopping due to context cancellation")
				return
			case <-ticker.C:
				// Process new items for symlinking
				item, err := s.db.GetNextItemForSymlinking()
				if err != nil {
					s.log.Errorf("Error getting next item for symlinking: %v", err)
					continue
				}
				if item != nil {
					s.log.Infof("Symlinking item: %s", item.Title)
					if err := s.symlinkItem(item); err != nil {
						s.log.Errorf("Error symlinking item: %v", err)
					}
				}
			}
		}
	}()

	return nil
}

// checkSymlinksInBackground checks symlinks in batches to avoid overwhelming the system
func (s *Symlinker) checkSymlinksInBackground(ctx context.Context) {
	s.log.Info("Starting background symlink check")
	defer func() {
		checkSymlinksLastRun = time.Now()
	}()

	// Check if repair is enabled
	if s.config.Programs.Symlinker.Repair == nil || !s.config.Programs.Symlinker.Repair.Enabled {
		s.log.Info("Symlink repair is disabled in config")
		return
	}

	// Use configured values or defaults
	batchSize := s.config.Programs.Symlinker.Repair.BatchSize
	if batchSize == 0 {
		batchSize = 250 // default batch size
	}

	batchDelay := s.config.Programs.Symlinker.Repair.BatchDelay
	if batchDelay == 0 {
		batchDelay = 50 * time.Millisecond // default delay
	}

	maxErrorsPerItem := s.config.Programs.Symlinker.Repair.MaxErrorsPerItem
	if maxErrorsPerItem == 0 {
		maxErrorsPerItem = 10 // default max errors to track
	}

	offset := 0
	checkedCount := 0
	repairedCount := 0
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("Background symlink check cancelled")
			return
		default:
		}

		// Check for items that are either downloaded or pending symlink
		items, err := s.db.GetItemsByStatusAndStep("downloading", "downloaded", "symlinking", "downloaded")
		if err != nil {
			s.log.Error("Failed to get items for symlinking", "error", err)
			return
		}

		if len(items) == 0 {
			s.log.Info("No items found for symlinking")
			return
		}

		batchStartTime := time.Now()
		for _, item := range items {
			if err := s.checkAndRepairSymlinks(item); err != nil {
				s.log.Error("Error checking/repairing symlinks",
					"item", item.Title,
					"error", err)
			}
			checkedCount++
		}
		batchDuration := time.Since(batchStartTime)

		// Log progress every 1000 items
		if checkedCount%1000 == 0 {
			s.log.Info("Symlink check progress",
				"itemsChecked", checkedCount,
				"itemsRepaired", repairedCount,
				"lastBatchDuration", batchDuration)
		}

		if len(items) < batchSize {
			break // Last batch
		}

		offset += batchSize
		time.Sleep(batchDelay)
	}

	totalDuration := time.Since(startTime)
	s.log.Info("Completed background symlink check",
		"itemsChecked", checkedCount,
		"itemsRepaired", repairedCount,
		"totalDuration", totalDuration,
		"itemsPerSecond", float64(checkedCount)/totalDuration.Seconds())
}

func (s *Symlinker) Stop() error {
	s.log.Info("Symlinker stopped")
	return nil
}

func (s *Symlinker) IsNeeded() bool {
	var count int
	err := s.db.QueryRow(`
		SELECT watchlistitem.id, watchlistitem.current_step, watchlistitem.media_type, scrape_results.status_results
		FROM watchlistitem
		LEFT JOIN scrape_results ON scrape_results.watchlist_item_id = watchlistitem.id
		LEFT JOIN seasons ON seasons.watchlist_item_id = watchlistitem.id
		LEFT JOIN tv_episodes ON tv_episodes.season_id = seasons.id
		WHERE 
			(
				watchlistitem.current_step IN ('downloading', 'downloaded', 'symlinking') 
				OR scrape_results.status_results = 'downloaded'
			)
		ORDER BY watchlistitem.requested_date ASC
		LIMIT 1;
    `).Scan(&count)

	if err != nil {
		s.log.Errorf("Error checking if symlinker is needed: %v", err)
		return false
	}

	s.log.Debugf("Found %d items with status 'downloaded' and associated downloaded scrape results", count)

	return count > 0
}

func (s *Symlinker) processNextItem() {
	item, err := s.db.GetNextItemForSymlinking()
	if err != nil {
		s.log.Errorf("Error getting next item for symlinking: %v", err)
		return
	}

	if item == nil {
		return // No items to process
	}

	s.log.Infof("Symlinking item: %s", item.Title)

	// Update item status to "symlinking"
	item.Status = sql.NullString{String: "symlinking", Valid: true}
	err = s.db.UpdateItemStatus(int64(item.ID), item.Status.String, item.CurrentStep.String)
	if err != nil {
		s.log.Errorf("Error updating item status: %v", err)
		return
	}

	// Perform symlinking
	err = s.symlinkItem(item)
	if err != nil {
		s.log.Errorf("Error symlinking item: %v", err)
		item.Status = sql.NullString{String: "symlink_failed", Valid: true}
	} else {
		item.Status = sql.NullString{String: "completed", Valid: true}
	}

	// Update item in database
	err = s.db.UpdateItemStatus(int64(item.ID), item.Status.String, item.CurrentStep.String)
	if err != nil {
		s.log.Errorf("Error updating item after symlinking: %v", err)
	}
}

func (s *Symlinker) findDownloadedFile(filename string) (string, error) {
	s.log.Info("Looking for downloaded file", "filename", filename)

	// Validate rclone path exists and is accessible
	if _, err := os.Stat(s.config.General.RclonePath); err != nil {
		if os.IsNotExist(err) {
			s.log.Error("RclonePath does not exist", "path", s.config.General.RclonePath)
			return "", fmt.Errorf("rclone path does not exist: %s", s.config.General.RclonePath)
		}
		if os.IsPermission(err) {
			s.log.Error("No permission to access RclonePath", "path", s.config.General.RclonePath)
			return "", fmt.Errorf("no permission to access rclone path: %s", s.config.General.RclonePath)
		}
		s.log.Error("Error accessing RclonePath", "path", s.config.General.RclonePath, "error", err)
		return "", fmt.Errorf("error accessing rclone path: %v", err)
	}

	// Walk through the rclone path to find the file
	var bestMatch string
	var bestSimilarity float64
	const similarityThreshold = 0.85 // 85% similarity threshold

	err := filepath.Walk(s.config.General.RclonePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				s.log.Warn("Permission denied accessing path", "path", path)
				return nil // Continue walking
			}
			return err
		}
		if !info.IsDir() {
			similarity := calculateSimilarity(info.Name(), filename)
			if similarity > bestSimilarity {
				bestSimilarity = similarity
				bestMatch = path
				s.log.Debug("Found potential match", "path", path, "similarity", similarity)
			}
		}
		return nil
	})

	if err != nil {
		s.log.Error("Error walking directory", "error", err)
		return "", fmt.Errorf("error scanning directory: %v", err)
	}

	if bestSimilarity >= similarityThreshold {
		s.log.Info("Found matching file", "path", bestMatch, "similarity", bestSimilarity)
		return bestMatch, nil
	}

	s.log.Warn("No matching file found", "filename", filename, "bestSimilarity", bestSimilarity)
	return "", fmt.Errorf("no matching file found for: %s (best similarity: %.2f)", filename, bestSimilarity)
}

func (s *Symlinker) formatDestinationName(item *database.WatchlistItem) string {
	var parts []string

	// Add title if valid
	if item.Title != "" {
		parts = append(parts, item.Title)
	}

	// For TV shows, include season/episode info from the filename
	if item.MediaType.Valid && item.MediaType.String == "tv" {
		// We'll get this from the scrape result filename later
		return strings.Join(parts, " ")
	}

	// For movies, include year if available
	if item.ItemYear.Valid {
		parts = append(parts, fmt.Sprintf("(%d)", item.ItemYear.Int64))
	}

	return strings.Join(parts, " ")
}

func (s *Symlinker) formatTVShowPath(baseDir string, item *database.WatchlistItem, filename string) string {
	// Extract season info from filename using regex
	seasonRegex := regexp.MustCompile(`[Ss](\d{1,2})`)
	seasonMatch := seasonRegex.FindStringSubmatch(filename)

	// Build the path: baseDir/Shows/ShowName/Season XX/
	showPath := filepath.Join(baseDir, "Shows", s.formatDestinationName(item))

	if len(seasonMatch) > 1 {
		seasonNum, _ := strconv.Atoi(seasonMatch[1])
		showPath = filepath.Join(showPath, fmt.Sprintf("Season %02d", seasonNum))
	}

	return showPath
}

func (s *Symlinker) updateItemStatus(item *database.WatchlistItem, symlinkCount int, symlinkErrors []error) error {
	if len(symlinkErrors) > 0 {
		// If we had some successful symlinks but also some errors
		if symlinkCount > 0 {
			if item.MediaType.Valid && item.MediaType.String == "tv" {
				// Check if show is ended/cancelled
				if item.ShowStatus.Valid && (item.ShowStatus.String == "Ended" ||
					item.ShowStatus.String == "Cancelled") {
					item.Status = sql.NullString{String: "completed", Valid: true}
				} else {
					item.Status = sql.NullString{String: "symlinked", Valid: true}
				}
			} else {
				item.Status = sql.NullString{String: "completed", Valid: true}
			}
			item.CurrentStep = sql.NullString{String: "symlinked", Valid: true}
			err := s.db.UpdateItemStatus(int64(item.ID), item.Status.String, item.CurrentStep.String)
			if err != nil {
				symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to update item status: %v", err))
			}
		} else {
			// If all symlinks failed
			item.Status = sql.NullString{String: "symlink_failed", Valid: true}
			err := s.db.UpdateItemStatus(int64(item.ID), item.Status.String, item.CurrentStep.String)
			if err != nil {
				symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to update item status: %v", err))
			}
			// Return the first error
			return symlinkErrors[0]
		}
	} else if symlinkCount > 0 {
		// All symlinks successful
		if item.MediaType.Valid && item.MediaType.String == "tv" {
			// Check if show is ended/cancelled
			if item.ShowStatus.Valid && (item.ShowStatus.String == "Ended" ||
				item.ShowStatus.String == "Cancelled") {
				item.Status = sql.NullString{String: "completed", Valid: true}
			} else {
				item.Status = sql.NullString{String: "symlinked", Valid: true}
			}
		} else {
			item.Status = sql.NullString{String: "completed", Valid: true}
		}
		item.CurrentStep = sql.NullString{String: "symlinked", Valid: true}
		err := s.db.UpdateItemStatus(int64(item.ID), item.Status.String, item.CurrentStep.String)
		if err != nil {
			return fmt.Errorf("failed to update item status: %v", err)
		}
	}

	return nil
}

func (s *Symlinker) symlinkItem(item *database.WatchlistItem) error {
	s.log.Info("Starting symlink process", "item", item.Title)

	// Log when an item is skipped
	if item.Category.Valid && item.Category.String == "movie" && item.Status.Valid && item.Status.String != "downloaded" {
		s.log.Debug("Skipping movie that hasn't been downloaded yet", "itemID", item.ID, "status", item.Status)
		return nil
	}

	// Get all scrape results that need symlinking
	scrapeResults, err := s.db.GetScrapeResultsForItem(int(item.ID))
	if err != nil {
		s.log.Error("Failed to get scrape results", "error", err)
		return fmt.Errorf("failed to get scrape results: %v", err)
	}

	s.log.Info("Fetched scrape results for item", "itemID", item.ID, "resultsCount", len(scrapeResults))

	if len(scrapeResults) == 0 {
		s.log.Warn("No scrape results found for item", "itemID", item.ID)
		return fmt.Errorf("no scrape results found for item %d", item.ID)
	}

	var symlinkErrors []error
	symlinkCount := 0

	for _, scrapeResult := range scrapeResults {
		// Log when processing each scrape result
		s.log.Info("Processing scrape result", "resultID", scrapeResult.ID)

		// Skip already symlinked results
		if scrapeResult.StatusResults.String == "symlinked" {
			s.log.Debug("Skipping already symlinked result", "resultID", scrapeResult.ID)
			continue
		}

		// Only process downloaded items
		if scrapeResult.StatusResults.String != "downloaded" {
			s.log.Debug("Skipping result that hasn't been downloaded yet", "resultID", scrapeResult.ID, "status", scrapeResult.StatusResults.String)
			continue
		}

		if !scrapeResult.ScrapedFilename.Valid {
			s.log.Warn("Invalid scraped filename", "resultID", scrapeResult.ID)
			continue
		}

		// Find the actual file
		sourcePath, err := s.findDownloadedFile(scrapeResult.ScrapedFilename.String)
		if err != nil {
			s.log.Error("Failed to find source file", "error", err)
			symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to find source file: %v", err))
			continue
		}

		// Verify source file exists and is accessible
		if _, err := os.Stat(sourcePath); err != nil {
			s.log.Error("Source file not accessible", "path", sourcePath, "error", err)
			symlinkErrors = append(symlinkErrors, fmt.Errorf("source file not accessible: %v", err))
			continue
		}

		// Get the file extension
		ext := filepath.Ext(sourcePath)

		var destPaths []string

		// Handle paths differently for TV shows and movies
		if item.MediaType.Valid && item.MediaType.String == "tv" {
			// Add main library path if set
			if s.config.General.LibraryPath != "" {
				showPath := s.formatTVShowPath(s.config.General.LibraryPath, item, scrapeResult.ScrapedFilename.String)
				destPaths = append(destPaths, filepath.Join(showPath, scrapeResult.ScrapedFilename.String))
			}

			// Handle custom libraries
			for _, lib := range s.config.CustomLibraries {
				if !lib.Active {
					continue
				}
				if s.itemMatchesCustomLibrary(item, lib) {
					showPath := s.formatTVShowPath(lib.Path, item, scrapeResult.ScrapedFilename.String)
					destPaths = append(destPaths, filepath.Join(showPath, scrapeResult.ScrapedFilename.String))

					if !lib.DuplicateInMainLibrary && len(destPaths) > 1 {
						destPaths = destPaths[1:] // Remove main library path
					}
					break
				}
			}
		} else {
			// Movie handling
			destName := s.formatDestinationName(item)

			if s.config.General.LibraryPath != "" {
				category := "Movies"
				if item.Category.Valid {
					category = strings.ToLower(item.Category.String)
				}
				mainLibPath := filepath.Join(s.config.General.LibraryPath, category, destName)
				destPaths = append(destPaths, filepath.Join(mainLibPath, destName+ext))
			}

			// Handle custom libraries for movies
			for _, lib := range s.config.CustomLibraries {
				if !lib.Active {
					continue
				}
				if s.itemMatchesCustomLibrary(item, lib) {
					customLibPath := filepath.Join(lib.Path, lib.Name, destName)
					destPaths = append(destPaths, filepath.Join(customLibPath, destName+ext))

					if !lib.DuplicateInMainLibrary && len(destPaths) > 1 {
						destPaths = destPaths[1:]
					}
					break
				}
			}
		}

		if len(destPaths) == 0 {
			s.log.Warn("No destination paths generated", "itemID", item.ID)
			symlinkErrors = append(symlinkErrors, fmt.Errorf("no destination paths generated"))
			continue
		}

		// Create symlinks
		for _, destPath := range destPaths {
			// Create the destination directory if it doesn't exist
			destDir := filepath.Dir(destPath)
			err := os.MkdirAll(destDir, 0755)
			if err != nil {
				s.log.Error("Failed to create destination directory", "path", destDir, "error", err)
				symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to create destination directory %s: %v", destDir, err))
				continue
			}

			// Check if destination already exists
			if _, err := os.Lstat(destPath); err == nil {
				s.log.Warn("Destination already exists, removing", "path", destPath)
				if err := os.Remove(destPath); err != nil {
					s.log.Error("Failed to remove existing destination", "path", destPath, "error", err)
					symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to remove existing destination %s: %v", destPath, err))
					continue
				}
			} else if !os.IsNotExist(err) {
				s.log.Error("Error checking destination", "path", destPath, "error", err)
				symlinkErrors = append(symlinkErrors, fmt.Errorf("error checking destination %s: %v", destPath, err))
				continue
			}

			// Create the symlink
			err = os.Symlink(sourcePath, destPath)
			if err != nil {
				s.log.Error("Failed to create symlink", "source", sourcePath, "dest", destPath, "error", err)
				symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to create symlink from %s to %s: %v", sourcePath, destPath, err))
				continue
			}

			s.log.Info("Created symlink", "source", sourcePath, "dest", destPath)
		}

		// Update scrape result status
		_, err = s.db.Exec(`
			UPDATE scraperesult 
			SET status_results = 'symlinked'
			WHERE id = $1
		`, scrapeResult.ID)
		if err != nil {
			s.log.Error("Failed to update scrape result status", "resultID", scrapeResult.ID, "error", err)
			symlinkErrors = append(symlinkErrors, fmt.Errorf("failed to update scrape result status: %v", err))
			continue
		}

		symlinkCount++
	}

	// Log when symlinking is completed
	s.log.Info("Completed symlinking process for item", "itemID", item.ID)

	// Update item status based on results
	err = s.updateItemStatus(item, symlinkCount, symlinkErrors)
	if err != nil {
		s.log.Error("Failed to update item status", "error", err)
		return fmt.Errorf("failed to update item status: %v", err)
	}

	s.log.Info("Completed symlinking", "itemID", item.ID, "symlinkCount", symlinkCount, "errorCount", len(symlinkErrors))
	return nil
}

func (s *Symlinker) itemMatchesCustomLibrary(item *database.WatchlistItem, lib config.CustomLibrary) bool {
	s.log.Info("Checking if item matches custom library")

	// Check include filters
	for _, filter := range lib.Filters.Include {
		if !s.checkFilter(item, filter) {
			s.log.Info("Item does not match include filter")
			return false
		}
	}

	// Check exclude filters
	for _, filter := range lib.Filters.Exclude {
		if s.checkFilter(item, filter) {
			s.log.Info("Item matches exclude filter")
			return false
		}
	}

	s.log.Info("Item matches custom library")
	return true
}

func (s *Symlinker) checkFilter(item *database.WatchlistItem, filter config.Filter) bool {
	switch filter.Type {
	case "genre":
		return s.checkGenre(item.Genres.String, filter.Value)
	case "rating":
		return s.checkRating(item.Rating.String, filter.Value)
	case "category":
		return strings.EqualFold(item.Category.String, filter.Value)
	default:
		s.log.Warnf("Unknown filter type: %s", filter.Type)
		return false
	}
}

func (s *Symlinker) checkGenre(itemGenres, filterValue string) bool {
	itemGenreList := strings.Split(strings.ToLower(itemGenres), ",")
	filterGenreList := strings.Split(strings.ToLower(filterValue), ",")

	for _, filterGenre := range filterGenreList {
		filterGenre = strings.TrimSpace(filterGenre)
		for _, itemGenre := range itemGenreList {
			itemGenre = strings.TrimSpace(itemGenre)
			if itemGenre == filterGenre {
				return true
			}
		}
	}
	return false
}

func (s *Symlinker) checkRating(itemRating, filterValue string) bool {
	filterRatings := strings.Split(filterValue, ",")
	for _, rating := range filterRatings {
		if strings.EqualFold(strings.TrimSpace(itemRating), strings.TrimSpace(rating)) {
			return true
		}
	}
	return false
}

func (s *Symlinker) sanitizeTitle(title string) string {
	// Remove any characters that are not allowed in file names
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ' ' {
			return r
		}
		return -1
	}, title)
}

func calculateSimilarity(s1, s2 string) float64 {
	s1 = strings.ToLower(s1)
	s2 = strings.ToLower(s2)

	// Calculate Levenshtein distance
	d := levenshteinDistance(s1, s2)

	// Convert distance to similarity score (0 to 1)
	maxLen := float64(max(len(s1), len(s2)))
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(d)/maxLen
}

func levenshteinDistance(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	// Create matrix
	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
	}

	// Initialize first row and column
	for i := 0; i <= len(s1); i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len(s2); j++ {
		matrix[0][j] = j
	}

	// Fill in the rest of the matrix
	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			if s1[i-1] == s2[j-1] {
				matrix[i][j] = matrix[i-1][j-1]
			} else {
				matrix[i][j] = min(
					matrix[i-1][j]+1,   // deletion
					matrix[i][j-1]+1,   // insertion
					matrix[i-1][j-1]+1, // substitution
				)
			}
		}
	}

	return matrix[len(s1)][len(s2)]
}

func min(nums ...int) int {
	if len(nums) == 0 {
		return 0
	}
	m := nums[0]
	for _, n := range nums[1:] {
		if n < m {
			m = n
		}
	}
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Symlinker) checkAndRepairSymlinks(item *database.WatchlistItem) error {
	s.log.Info("Checking symlinks", "item", item.Title)

	// Get all scrape results for this item
	scrapeResults, err := s.db.GetScrapeResultsForItem(int(item.ID))
	if err != nil {
		s.log.Error("Failed to get scrape results", "error", err)
		return err
	}

	var repairErrors []error
	repairCount := 0

	for _, scrapeResult := range scrapeResults {
		// Only check symlinked results
		if scrapeResult.StatusResults.String != "symlinked" {
			continue
		}

		// Find all symlinks for this scrape result
		symlinks, err := s.findSymlinksForScrapeResult(item, scrapeResult)
		if err != nil {
			s.log.Error("Failed to find symlinks", "error", err)
			repairErrors = append(repairErrors, fmt.Errorf("failed to find symlinks: %v", err))
			continue
		}

		for _, symlinkPath := range symlinks {
			// Check if symlink exists and is valid
			linkInfo, err := os.Lstat(symlinkPath)
			if err != nil {
				if os.IsNotExist(err) {
					s.log.Info("Symlink missing, will recreate", "path", symlinkPath)
				} else {
					s.log.Error("Error checking symlink", "path", symlinkPath, "error", err)
					repairErrors = append(repairErrors, fmt.Errorf("error checking symlink %s: %v", symlinkPath, err))
					continue
				}
			} else if linkInfo.Mode()&os.ModeSymlink == 0 {
				s.log.Warn("Path exists but is not a symlink", "path", symlinkPath)
				continue
			} else {
				// Check if the symlink target is valid
				target, err := os.Readlink(symlinkPath)
				if err != nil {
					s.log.Error("Error reading symlink target", "path", symlinkPath, "error", err)
					repairErrors = append(repairErrors, fmt.Errorf("error reading symlink target %s: %v", symlinkPath, err))
					continue
				}

				// Check if target exists
				if _, err := os.Stat(target); err == nil {
					s.log.Debug("Symlink is valid", "path", symlinkPath, "target", target)
					continue // Symlink is valid
				}
			}

			// At this point, we need to repair the symlink
			// First, try to find the source file again
			sourcePath, err := s.findDownloadedFile(scrapeResult.ScrapedFilename.String)
			if err != nil {
				s.log.Error("Failed to find source file for repair", "error", err)
				repairErrors = append(repairErrors, fmt.Errorf("failed to find source file for repair: %v", err))
				continue
			}

			// Remove existing broken symlink if it exists
			if err := os.Remove(symlinkPath); err != nil && !os.IsNotExist(err) {
				s.log.Error("Failed to remove broken symlink", "path", symlinkPath, "error", err)
				repairErrors = append(repairErrors, fmt.Errorf("failed to remove broken symlink %s: %v", symlinkPath, err))
				continue
			}

			// Create directory if it doesn't exist
			if err := os.MkdirAll(filepath.Dir(symlinkPath), 0755); err != nil {
				s.log.Error("Failed to create directory for repair", "path", filepath.Dir(symlinkPath), "error", err)
				repairErrors = append(repairErrors, fmt.Errorf("failed to create directory for repair %s: %v", filepath.Dir(symlinkPath), err))
				continue
			}

			// Create new symlink
			if err := os.Symlink(sourcePath, symlinkPath); err != nil {
				s.log.Error("Failed to create repaired symlink", "source", sourcePath, "dest", symlinkPath, "error", err)
				repairErrors = append(repairErrors, fmt.Errorf("failed to create repaired symlink from %s to %s: %v", sourcePath, symlinkPath, err))
				continue
			}

			s.log.Info("Repaired symlink", "path", symlinkPath, "source", sourcePath)
			repairCount++
		}
	}

	if len(repairErrors) > 0 {
		s.log.Warn("Completed symlink repair with errors",
			"itemID", item.ID,
			"repairCount", repairCount,
			"errorCount", len(repairErrors))
		return fmt.Errorf("completed with %d repairs and %d errors", repairCount, len(repairErrors))
	}

	s.log.Info("Completed symlink repair", "itemID", item.ID, "repairCount", repairCount)
	return nil
}

func (s *Symlinker) findSymlinksForScrapeResult(item *database.WatchlistItem, scrapeResult *database.ScrapeResult) ([]string, error) {
	var destPaths []string
	ext := filepath.Ext(scrapeResult.ScrapedFilename.String)

	// Handle paths differently for TV shows and movies
	if item.MediaType.Valid && item.MediaType.String == "tv" {
		// Add main library path if set
		if s.config.General.LibraryPath != "" {
			showPath := s.formatTVShowPath(s.config.General.LibraryPath, item, scrapeResult.ScrapedFilename.String)
			destPaths = append(destPaths, filepath.Join(showPath, scrapeResult.ScrapedFilename.String))
		}

		// Handle custom libraries
		for _, lib := range s.config.CustomLibraries {
			if !lib.Active {
				continue
			}
			if s.itemMatchesCustomLibrary(item, lib) {
				showPath := s.formatTVShowPath(lib.Path, item, scrapeResult.ScrapedFilename.String)
				destPaths = append(destPaths, filepath.Join(showPath, scrapeResult.ScrapedFilename.String))

				if !lib.DuplicateInMainLibrary && len(destPaths) > 1 {
					destPaths = destPaths[1:] // Remove main library path
				}
				break
			}
		}
	} else {
		// Movie handling
		destName := s.formatDestinationName(item)

		if s.config.General.LibraryPath != "" {
			category := "Movies"
			if item.Category.Valid {
				category = strings.ToLower(item.Category.String)
			}
			mainLibPath := filepath.Join(s.config.General.LibraryPath, category, destName)
			destPaths = append(destPaths, filepath.Join(mainLibPath, destName+ext))
		}

		// Handle custom libraries for movies
		for _, lib := range s.config.CustomLibraries {
			if !lib.Active {
				continue
			}
			if s.itemMatchesCustomLibrary(item, lib) {
				customLibPath := filepath.Join(lib.Path, lib.Name, destName)
				destPaths = append(destPaths, filepath.Join(customLibPath, destName+ext))

				if !lib.DuplicateInMainLibrary && len(destPaths) > 1 {
					destPaths = destPaths[1:]
				}
				break
			}
		}
	}

	if len(destPaths) == 0 {
		return nil, fmt.Errorf("no destination paths found")
	}

	return destPaths, nil
}
