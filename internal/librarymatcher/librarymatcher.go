package librarymatcher

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type LibraryMatcher struct {
	db     *database.DB
	log    *logger.Logger
	config *config.Config
}

func NewLibraryMatcher(cfg *config.Config, db *database.DB) *LibraryMatcher {
	return &LibraryMatcher{
		db:     db,
		log:    logger.New(),
		config: cfg,
	}
}

func New(cfg *config.Config, db *database.DB) *LibraryMatcher {
	return NewLibraryMatcher(cfg, db)
}

func (lm *LibraryMatcher) Start(ctx context.Context) error {
	lm.log.Info("LibraryMatcher", "Start", "Starting LibraryMatcher")
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				lm.ProcessNextItem()
				time.Sleep(5 * time.Second) // Adjust this delay as needed
			}
		}
	}()
	return nil
}

func (lm *LibraryMatcher) Stop() error {
	lm.log.Info("LibraryMatcher", "Stop", "Stopping LibraryMatcher")
	return nil
}

func (lm *LibraryMatcher) Name() string {
	return "library_matcher"
}

func (lm *LibraryMatcher) IsNeeded() bool {
	var count int
	err := lm.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem 
        WHERE status = 'new' 
        AND current_step = 'match_pending'
    `).Scan(&count)

	return err == nil && count > 0
}

func (lm *LibraryMatcher) ProcessNextItem() {
	item, err := lm.db.GetNextItemForLibraryMatching()
	if err != nil {
		lm.log.Error("LibraryMatcher", "ProcessNextItem", fmt.Sprintf("Error getting next item for library matching: %v", err))
		return
	}

	if item == nil {
		lm.log.Info("LibraryMatcher", "ProcessNextItem", "No items available for library matching")
		return
	}

	lm.log.Info("LibraryMatcher", "ProcessNextItem", fmt.Sprintf("Matching library for item: %s", item.Title))

	// Perform library matching
	matchedLibraries := lm.matchLibraries(item)
	if len(matchedLibraries) > 0 {
		item.CustomLibrary = sql.NullString{String: strings.Join(matchedLibraries, ","), Valid: true}
		item.Status = sql.NullString{String: "library_matched", Valid: true}
		lm.log.Info("LibraryMatcher", "ProcessNextItem", fmt.Sprintf("Matched item to custom libraries: %s", strings.Join(matchedLibraries, ", ")))
	} else {
		item.CustomLibrary = sql.NullString{String: "", Valid: false}
		item.Status = sql.NullString{String: "ready_for_scraping", Valid: true}
		lm.log.Info("LibraryMatcher", "ProcessNextItem", "No custom library match found for item, proceeding to next step")
	}

	// Update item in database
	err = lm.db.UpdateWatchlistItemForLibraryMatching(item)
	if err != nil {
		lm.log.Error("LibraryMatcher", "ProcessNextItem", fmt.Sprintf("Error updating item after library matching: %v", err))
		return
	}
}

func (lm *LibraryMatcher) matchLibraries(item *database.WatchlistItem) []string {
	matchedLibraries := []string{}
	for _, lib := range lm.config.CustomLibraries {
		if lib.Active && lm.itemMatchesLibrary(item, lib) {
			matchedLibraries = append(matchedLibraries, lib.Name)
			lm.log.Info("LibraryMatcher", "matchLibraries", fmt.Sprintf("Matched item to custom library: %s", lib.Name))
		}
	}
	return matchedLibraries
}

func (lm *LibraryMatcher) itemMatchesLibrary(item *database.WatchlistItem, lib config.CustomLibrary) bool {
	// Check include filters
	for _, filter := range lib.Filters.Include {
		if !lm.checkFilter(item, filter) {
			lm.log.Debug("LibraryMatcher", "itemMatchesLibrary", fmt.Sprintf("Item %s does not match include filter: %v", item.Title, filter))
			return false
		}
	}

	// Check exclude filters
	for _, filter := range lib.Filters.Exclude {
		if lm.checkFilter(item, filter) {
			lm.log.Debug("LibraryMatcher", "itemMatchesLibrary", fmt.Sprintf("Item %s matches exclude filter: %v", item.Title, filter))
			return false
		}
	}

	return true
}

func (lm *LibraryMatcher) checkFilter(item *database.WatchlistItem, filter config.Filter) bool {
	switch filter.Type {
	case "genre":
		match := lm.checkGenre(item.Genres.String, filter.Value)
		if match {
			lm.log.Debug("LibraryMatcher", "checkFilter", fmt.Sprintf("Genre match: %s against %s", item.Genres.String, filter.Value))
		}
		return match
	case "rating":
		match := lm.checkRating(item.Rating.String, filter.Value)
		if match {
			lm.log.Debug("LibraryMatcher", "checkFilter", fmt.Sprintf("Rating match: %s against %s", item.Rating.String, filter.Value))
		}
		return match
	case "category":
		match := strings.EqualFold(item.Category.String, filter.Value)
		if match {
			lm.log.Debug("LibraryMatcher", "checkFilter", fmt.Sprintf("Category match: %s against %s", item.Category.String, filter.Value))
		}
		return match
	case "resolution":
		return lm.checkResolution(item.BestScrapedResolution.String, filter.Value)
	case "codec":
		return lm.checkCodec(item.BestScrapedFilename.String, filter.Value) // We'll check the filename for codec info
	default:
		lm.log.Warning("LibraryMatcher", "checkFilter", fmt.Sprintf("Unknown filter type: %s", filter.Type))
		return false
	}
}

func (lm *LibraryMatcher) checkRating(itemRating, filterValue string) bool {
	ratings := strings.Split(filterValue, ",")
	for _, rating := range ratings {
		if strings.EqualFold(strings.TrimSpace(rating), itemRating) {
			return true
		}
	}
	return false
}

func (lm *LibraryMatcher) checkResolution(itemResolution, filterValue string) bool { // Changed function name from checkQuality to checkResolution
	resolutions := strings.Split(filterValue, ",")
	for _, resolution := range resolutions {
		if strings.Contains(strings.ToLower(itemResolution), strings.ToLower(strings.TrimSpace(resolution))) {
			return true
		}
	}
	return false
}

func (lm *LibraryMatcher) checkCodec(itemCodec, filterValue string) bool {
	codecs := strings.Split(filterValue, ",")
	for _, codec := range codecs {
		if strings.Contains(strings.ToLower(itemCodec), strings.ToLower(strings.TrimSpace(codec))) {
			return true
		}
	}
	return false
}

func (lm *LibraryMatcher) checkGenre(itemGenres, filterValue string) bool {
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

func (lm *LibraryMatcher) ProcessItemByID(itemID int) {
	item, err := lm.db.GetWatchlistItemByID(itemID)
	if err != nil {
		lm.log.Error("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("Error getting item by ID: %v", err))
		return
	}

	if item == nil {
		lm.log.Info("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("No item found with ID: %d", itemID))
		return
	}

	lm.log.Info("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("Matching library for item: %s", item.Title))

	// Perform library matching
	matchedLibraries := lm.matchLibraries(item)
	if len(matchedLibraries) > 0 {
		item.CustomLibrary = sql.NullString{String: strings.Join(matchedLibraries, ","), Valid: true}
		item.Status = sql.NullString{String: "library_matched", Valid: true}
		item.CurrentStep = sql.NullString{String: "scraping_pending", Valid: true}
		lm.log.Info("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("Matched item to custom libraries: %s", strings.Join(matchedLibraries, ", ")))

		// Check if any matched library requires duplication in the main library
		duplicateInMainLibrary := false
		for _, libName := range matchedLibraries {
			for _, lib := range lm.config.CustomLibraries {
				if lib.Name == libName && lib.DuplicateInMainLibrary {
					duplicateInMainLibrary = true
					break
				}
			}
		}
		item.MainLibraryPath = sql.NullString{String: fmt.Sprintf("%t", duplicateInMainLibrary), Valid: true}
		lm.log.Debug("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("Setting MainLibraryPath to: %s", item.MainLibraryPath.String))
	} else {
		item.CustomLibrary = sql.NullString{String: "", Valid: false}
		item.Status = sql.NullString{String: "matcher_failed", Valid: true}
		item.CurrentStep = sql.NullString{String: "matching", Valid: true}
		item.MainLibraryPath = sql.NullString{String: "false", Valid: true}
		lm.log.Debug("LibraryMatcher", "ProcessItemByID", "No match found, setting MainLibraryPath to: false")
	}

	// Update item in database
	err = lm.db.UpdateWatchlistItemForLibraryMatching(item)
	if err != nil {
		lm.log.Error("LibraryMatcher", "ProcessItemByID", fmt.Sprintf("Error updating item after library matching: %v", err))
		return
	}
}

// Match processes a single item for library matching
func (lm *LibraryMatcher) Match(item *database.WatchlistItem) error {
	lm.log.Info("LibraryMatcher", "Match", fmt.Sprintf("Matching library for item: %s", item.Title))

	// Perform library matching
	matchedLibraries := lm.matchLibraries(item)
	if len(matchedLibraries) > 0 {
		// Get the first matched library's configuration
		var duplicateInMainLibrary bool
		for _, lib := range lm.config.CustomLibraries {
			if lib.Name == matchedLibraries[0] {
				duplicateInMainLibrary = lib.DuplicateInMainLibrary
				break
			}
		}

		item.CustomLibrary = sql.NullString{String: strings.Join(matchedLibraries, ","), Valid: true}
		item.Status = sql.NullString{String: "library_matched", Valid: true}
		item.CurrentStep = sql.NullString{String: "scraping_pending", Valid: true}
		
		// Set main_library_path based on duplicate_in_main_library setting
		mainLibraryPath := "false"
		if duplicateInMainLibrary {
			mainLibraryPath = "true"
		}
		item.MainLibraryPath = sql.NullString{String: mainLibraryPath, Valid: true}
		
		lm.log.Info("LibraryMatcher", "Match", fmt.Sprintf("Matched item to custom libraries: %s (main_library_path: %s)", 
			strings.Join(matchedLibraries, ", "), mainLibraryPath))
	} else {
		item.CustomLibrary = sql.NullString{String: "", Valid: false}
		item.Status = sql.NullString{String: "library_matched", Valid: true}
		item.CurrentStep = sql.NullString{String: "scraping_pending", Valid: true}
		
		// If no custom libraries matched, set main_library_path to true
		item.MainLibraryPath = sql.NullString{String: "true", Valid: true}
		
		lm.log.Info("LibraryMatcher", "Match", "No custom library match found for item, proceeding to scraping")
	}

	// Update item in database
	err := lm.db.UpdateWatchlistItemForLibraryMatching(item)
	if err != nil {
		return fmt.Errorf("error updating item after library matching: %v", err)
	}

	return nil
}
