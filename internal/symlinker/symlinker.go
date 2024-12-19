package symlinker

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"mye-r/internal/config"
	"mye-r/internal/database"
	"os"
	"path/filepath"
	"strings"
)

// DBInterface defines the database methods needed by the symlinker
type DBInterface interface {
	GetNextItemForSymlinking() (*database.WatchlistItem, error)
	UpdateWatchlistItem(*database.WatchlistItem) error
	GetLatestScrapeResult(int) (*database.ScrapeResult, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Exec(query string, args ...interface{}) (sql.Result, error)
}

type Symlinker struct {
	config *config.Config
	db     DBInterface
}

func New(cfg *config.Config, db DBInterface) *Symlinker {
	return NewSymlinker(cfg, db)
}

func NewSymlinker(cfg *config.Config, db DBInterface) *Symlinker {
	return &Symlinker{
		config: cfg,
		db:     db,
	}
}

func (s *Symlinker) Name() string {
	return "symlinker"
}

func (s *Symlinker) Start(ctx context.Context) error {
	log.Println("Symlinker started")

	item, err := s.db.GetNextItemForSymlinking()
	if err != nil {
		log.Printf("Error getting next item for symlinking: %v", err)
		return err
	}
	if item != nil {
		log.Printf("Symlinking item: %s", item.Title)
		if err := s.symlinkItem(item); err != nil {
			log.Printf("Error symlinking item: %v", err)
			return err
		}
	} else {
		log.Printf("No items to process (status='downloaded' and current_step='symlink_pending')")
	}

	return nil
}

func (s *Symlinker) Stop() error {
	log.Println("Symlinker stopped")
	return nil
}

func (s *Symlinker) IsNeeded() bool {
	var count int
	err := s.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem 
        WHERE status = 'downloaded' 
        AND current_step = 'symlink_pending'
    `).Scan(&count)

	return err == nil && count > 0
}

func (s *Symlinker) processNextItem() {
	item, err := s.db.GetNextItemForSymlinking()
	if err != nil {
		log.Printf("Error getting next item for symlinking: %v", err)
		return
	}

	if item == nil {
		return // No items to process
	}

	log.Printf("Symlinking item: %s", item.Title)

	// Update item status to "symlinking"
	item.Status = sql.NullString{String: "symlinking", Valid: true}
	err = s.db.UpdateWatchlistItem(item)
	if err != nil {
		log.Printf("Error updating item status: %v", err)
		return
	}

	// Perform symlinking
	err = s.symlinkItem(item)
	if err != nil {
		log.Printf("Error symlinking item: %v", err)
		item.Status = sql.NullString{String: "symlink_failed", Valid: true}
	} else {
		item.Status = sql.NullString{String: "completed", Valid: true}
	}

	// Update item in database
	err = s.db.UpdateWatchlistItem(item)
	if err != nil {
		log.Printf("Error updating item after symlinking: %v", err)
	}
}

func (s *Symlinker) findDownloadedFile(filename string) (string, error) {
	log.Printf("Looking for file: %s in path: %s", filename, s.config.General.RclonePath)

	// Walk through the rclone path to find the file
	var bestMatch string
	var bestSimilarity float64
	const similarityThreshold = 0.85 // 85% similarity threshold

	err := filepath.Walk(s.config.General.RclonePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			similarity := calculateSimilarity(info.Name(), filename)
			log.Printf("Checking file: %s (similarity: %.2f)", info.Name(), similarity)
			if similarity > bestSimilarity {
				bestSimilarity = similarity
				bestMatch = path
			}
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("error walking directory: %v", err)
	}

	if bestSimilarity >= similarityThreshold {
		log.Printf("Found best match: %s (similarity: %.2f)", bestMatch, bestSimilarity)
		return bestMatch, nil
	}

	return "", fmt.Errorf("file not found: %s (best match had similarity: %.2f)", filename, bestSimilarity)
}

func (s *Symlinker) formatDestinationName(item *database.WatchlistItem) string {
	// Base name: Title (Year) {IMDB_ID}
	baseName := s.sanitizeTitle(item.Title)
	if item.ItemYear.Valid {
		baseName += fmt.Sprintf(" (%d)", item.ItemYear.Int64)
	}
	if item.ImdbID.Valid {
		baseName += fmt.Sprintf(" {%s}", item.ImdbID.String)
	}
	return baseName
}

func (s *Symlinker) symlinkItem(item *database.WatchlistItem) error {
	scrapeResult, err := s.db.GetLatestScrapeResult(item.ID)
	if err != nil {
		return fmt.Errorf("failed to get scrape result: %v", err)
	}
	if scrapeResult == nil || !scrapeResult.ScrapedFilename.Valid {
		return fmt.Errorf("no valid scrape result found")
	}

	log.Printf("Got scrape result for item %d: %+v", item.ID, scrapeResult)
	log.Printf("Looking for filename: %s", scrapeResult.ScrapedFilename.String)

	// Find the actual file
	sourcePath, err := s.findDownloadedFile(scrapeResult.ScrapedFilename.String)
	if err != nil {
		return fmt.Errorf("failed to find source file: %v", err)
	}

	// Get the file extension
	ext := filepath.Ext(sourcePath)

	// Format the destination name
	destName := s.formatDestinationName(item)

	// Determine the destination paths (main library and custom libraries)
	var destPaths []string

	// Add main library path if set
	if s.config.General.LibraryPath != "" {
		category := "unknown"
		if item.Category.Valid {
			category = strings.ToLower(item.Category.String)
		}
		mainLibPath := filepath.Join(s.config.General.LibraryPath, category, destName)
		destPaths = append(destPaths, filepath.Join(mainLibPath, destName+ext))
	}

	// Check custom libraries
	for _, lib := range s.config.CustomLibraries {
		if !lib.Active {
			continue
		}
		log.Printf("Checking if item matches custom library: %s", lib.Name)
		if s.itemMatchesCustomLibrary(item, lib) {
			log.Printf("Item matches custom library: %s", lib.Name)
			// Include library name in the path
			customLibPath := filepath.Join(lib.Path, lib.Name, destName)
			destPaths = append(destPaths, filepath.Join(customLibPath, destName+ext))

			if !lib.DuplicateInMainLibrary && len(destPaths) > 1 {
				// Remove main library path if not duplicating
				destPaths = destPaths[1:]
			}
			break // Assuming an item can only match one custom library
		} else {
			log.Printf("Item does not match custom library: %s", lib.Name)
		}
	}

	// Create symlinks
	for _, destPath := range destPaths {
		// Create the destination directory if it doesn't exist
		destDir := filepath.Dir(destPath)
		err := os.MkdirAll(destDir, 0755)
		if err != nil {
			return fmt.Errorf("failed to create destination directory %s: %v", destDir, err)
		}

		// Create the symlink
		err = os.Symlink(sourcePath, destPath)
		if err != nil {
			return fmt.Errorf("failed to create symlink %s -> %s: %v", destPath, sourcePath, err)
		}

		log.Printf("Created symlink: %s -> %s", destPath, sourcePath)
	}

	// Update item status and current_step
	_, err = s.db.Exec(`
        UPDATE watchlistitem 
        SET status = 'completed', current_step = 'symlinked'
        WHERE id = $1
    `, item.ID)
	if err != nil {
		return fmt.Errorf("failed to update item status: %v", err)
	}
	log.Printf("Updated item %d status to completed and current_step to symlinked", item.ID)

	return nil
}

func (s *Symlinker) itemMatchesCustomLibrary(item *database.WatchlistItem, lib config.CustomLibrary) bool {
	log.Printf("Checking if item matches custom library: %s", lib.Name)

	// Check include filters
	for _, filter := range lib.Filters.Include {
		if !s.checkFilter(item, filter) {
			log.Printf("Item does not match include filter: %+v", filter)
			return false
		}
	}

	// Check exclude filters
	for _, filter := range lib.Filters.Exclude {
		if s.checkFilter(item, filter) {
			log.Printf("Item matches exclude filter: %+v", filter)
			return false
		}
	}

	log.Printf("Item matches custom library: %s", lib.Name)
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
		log.Printf("Unknown filter type: %s", filter.Type)
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
