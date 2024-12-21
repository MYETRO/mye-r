package getcontent

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type PlexRSSFetcher struct {
	cfg  *config.Config
	db   *database.DB
	log  *logger.Logger
	stop chan struct{}
}

type MediaKeywords struct {
	Keywords string `xml:",chardata"`
}

type MediaRating struct {
	Scheme string `xml:"scheme,attr"`
	Rating string `xml:",chardata"`
}

func NewPlexRSSFetcher(cfg *config.Config, db *database.DB) *PlexRSSFetcher {
	log := logger.New()
	log.Info("PlexRSSFetcher", "NewPlexRSSFetcher", "Creating new PlexRSSFetcher instance")
	fetcher := &PlexRSSFetcher{
		cfg:  cfg,
		db:   db,
		log:  log,
		stop: make(chan struct{}),
	}
	log.Info("PlexRSSFetcher", "NewPlexRSSFetcher", "PlexRSSFetcher instance created successfully")
	return fetcher
}

func (f *PlexRSSFetcher) Start(ctx context.Context) {
	f.log.Info("PlexRSSFetcher", "Start", "Starting PlexRSSFetcher")
	plexRSSConfig, ok := f.cfg.Fetchers["plexrss"]
	if !ok || !plexRSSConfig.Enabled {
		f.log.Warning("PlexRSSFetcher", "Start", "PlexRSSFetcher not enabled or not configured")
		return
	}

	f.log.Info("PlexRSSFetcher", "Start", fmt.Sprintf("PlexRSSFetcher configured with interval: %d minutes", plexRSSConfig.Interval))
	f.log.Info("PlexRSSFetcher", "Start", fmt.Sprintf("Configured URLs: %v", plexRSSConfig.URLs))

	// Perform initial fetch
	f.log.Info("PlexRSSFetcher", "Start", "Performing initial fetch")
	for _, url := range plexRSSConfig.URLs {
		f.log.Info("PlexRSSFetcher", "Start", fmt.Sprintf("Fetching from URL: %s", url))
		err := f.fetchWithCustomParser(url)
		if err != nil {
			f.log.Error("PlexRSSFetcher", "Start", fmt.Sprintf("Error fetching from URL %s: %v", url, err))
		}
	}

	ticker := time.NewTicker(time.Duration(plexRSSConfig.Interval) * time.Minute)
	defer ticker.Stop()

	f.log.Info("PlexRSSFetcher", "Start", "Entering main loop")
	for {
		select {
		case <-ctx.Done():
			f.log.Info("PlexRSSFetcher", "Start", "Stopping due to context cancellation")
			return
		case <-f.stop:
			f.log.Info("PlexRSSFetcher", "Start", "Stopping due to stop signal")
			return
		case <-ticker.C:
			f.log.Info("PlexRSSFetcher", "Start", "Ticker triggered, starting fetch process")
			for _, url := range plexRSSConfig.URLs {
				f.log.Info("PlexRSSFetcher", "Start", fmt.Sprintf("Fetching from URL: %s", url))
				err := f.fetchWithCustomParser(url)
				if err != nil {
					f.log.Error("PlexRSSFetcher", "Start", fmt.Sprintf("Error fetching from URL %s: %v", url, err))
				}
			}
			f.log.Info("PlexRSSFetcher", "Start", "Fetch process completed")
		}
	}
}

func (f *PlexRSSFetcher) Stop() {
	f.log.Info("PlexRSSFetcher", "Stop", "Stopping PlexRSSFetcher")
	close(f.stop)
	f.log.Info("PlexRSSFetcher", "Stop", "PlexRSSFetcher stopped")
}

func (f *PlexRSSFetcher) fetchWithCustomParser(url string) error {
	f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Starting fetch from URL: %s", url))

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("error fetching RSS feed: %v", err)
	}
	defer resp.Body.Close()

	f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", "Successfully fetched RSS feed, starting to parse")

	decoder := xml.NewDecoder(resp.Body)
	var currentItem *database.WatchlistItem
	itemCount := 0

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.log.Error("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Error decoding XML: %v", err))
			return fmt.Errorf("error decoding XML: %v", err)
		}

		switch elem := tok.(type) {
		case xml.StartElement:
			if elem.Name.Local == "item" {
				f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", "Found new item, starting to parse")
				currentItem = &database.WatchlistItem{}
				itemCount++
			}
			if currentItem != nil {
				switch {
				case elem.Name.Local == "title":
					var title string
					decoder.DecodeElement(&title, &elem)
					currentItem.Title, currentItem.ItemYear = extractTitleAndYear(title)
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed title: %s, year: %d", currentItem.Title, currentItem.ItemYear.Int64))
				case elem.Name.Local == "link":
					var link string
					decoder.DecodeElement(&link, &elem)
					currentItem.Link = sql.NullString{String: link, Valid: true}
				case elem.Name.Local == "pubDate":
					var pubDate string
					decoder.DecodeElement(&pubDate, &elem)
					parsedDate, err := time.Parse(time.RFC1123, pubDate)
					if err == nil {
						currentItem.RequestedDate = parsedDate.Truncate(time.Second)
					}
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed pubDate: %s", currentItem.RequestedDate))
				case elem.Name.Local == "guid":
					var guid string
					decoder.DecodeElement(&guid, &elem)
					currentItem.ImdbID, currentItem.TmdbID, currentItem.TvdbID = extractIDs(guid)
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed IDs - IMDB: %s, TMDB: %s, TVDB: %s", currentItem.ImdbID.String, currentItem.TmdbID.String, currentItem.TvdbID.String))
				case elem.Name.Local == "description":
					var desc string
					decoder.DecodeElement(&desc, &elem)
					currentItem.Description = sql.NullString{String: desc, Valid: true}
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed description: %s", currentItem.Description.String))
				case elem.Name.Local == "category":
					var category string
					decoder.DecodeElement(&category, &elem)
					f.log.Debug("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Raw category value: '%s'", category))
					currentItem.Category = sql.NullString{String: category, Valid: true}
					// Set media_type based on category
					if strings.ToLower(category) == "show" {
						currentItem.MediaType = sql.NullString{String: "tv", Valid: true}
					} else if strings.ToLower(category) == "movie" {
						currentItem.MediaType = sql.NullString{String: "movie", Valid: true}
					}
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed category: %s, set media_type: %s", currentItem.Category.String, currentItem.MediaType.String))
				case elem.Name.Local == "keywords" && elem.Name.Space == "http://search.yahoo.com/mrss/":
					var keywords string
					decoder.DecodeElement(&keywords, &elem)
					currentItem.Genres = sql.NullString{String: keywords, Valid: keywords != ""}
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed keywords (genres): %s", keywords))
				case elem.Name.Local == "rating" && elem.Name.Space == "http://search.yahoo.com/mrss/":
					var rating string
					decoder.DecodeElement(&rating, &elem)
					currentItem.Rating = sql.NullString{String: rating, Valid: rating != ""}
					f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Parsed rating: %s", rating))
				case elem.Name.Local == "thumbnail" && elem.Name.Space == "http://search.yahoo.com/mrss/":
					for _, attr := range elem.Attr {
						if attr.Name.Local == "url" {
							currentItem.ThumbnailURL = sql.NullString{String: attr.Value, Valid: attr.Value != ""}
							break
						}
					}
				case elem.Name.Local == "media:keywords":
					var keywords MediaKeywords
					err := decoder.DecodeElement(&keywords, &elem)
					if err != nil {
						f.log.Error("PlexRSSFetcher", "parseElement", fmt.Sprintf("Error parsing media:keywords: %v", err))
					} else {
						currentItem.Genres = sql.NullString{String: keywords.Keywords, Valid: true}
					}
				case elem.Name.Local == "media:rating":
					var rating MediaRating
					err := decoder.DecodeElement(&rating, &elem)
					if err != nil {
						f.log.Error("PlexRSSFetcher", "parseElement", fmt.Sprintf("Error parsing media:rating: %v", err))
					} else {
						currentItem.Rating = sql.NullString{String: rating.Rating, Valid: true}
					}
				}
			}
		case xml.EndElement:
			if elem.Name.Local == "item" && currentItem != nil {
				f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", "Finished parsing item, processing it")
				f.processCustomParsedItem(currentItem)
				currentItem = nil
			}
		}
	}

	f.log.Info("PlexRSSFetcher", "fetchWithCustomParser", fmt.Sprintf("Finished parsing RSS feed. Total items processed: %d", itemCount))
	return nil
}

func (f *PlexRSSFetcher) processCustomParsedItem(item *database.WatchlistItem) {
	// First try to find by IDs
	existingItem, err := f.db.FindWatchlistItemByIDs(item.ImdbID.String, item.TmdbID.String, item.TvdbID.String)
	if err != nil {
		f.log.Error("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Error checking if item exists in database by IDs: %v", err))
		return
	}

	// If not found by IDs, try to find by title and year
	if existingItem == nil && item.ItemYear.Valid {
		existingItem, err = f.db.FindWatchlistItemByTitleAndYear(item.Title, item.ItemYear.Int64)
		if err != nil {
			f.log.Error("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Error checking if item exists in database by title and year: %v", err))
			return
		}
	}

	f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Preparing to process item: Title: %s, ItemYear: %d, ImdbID: %s, TmdbID: %s, TvdbID: %s",
		item.Title, item.ItemYear.Int64, item.ImdbID.String, item.TmdbID.String, item.TvdbID.String))

	if existingItem == nil {
		f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("New item found: %s (%d)", item.Title, item.ItemYear.Int64))

		item.CurrentStep = sql.NullString{String: "new", Valid: true} // Set to first step for TMDB indexing
		f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Setting current_step to: %s (valid: %v)", item.CurrentStep.String, item.CurrentStep.Valid))
		item.CreatedAt = time.Now()
		item.UpdatedAt = time.Now()

		err = f.db.CreateWatchlistItem(item)
		if err != nil {
			f.log.Error("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Error adding new item to database: %v", err))
			return
		}

		f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Successfully added new item to watchlist: %s (%d) with current_step: %s", item.Title, item.ItemYear.Int64, item.CurrentStep.String))
	} else {
		f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Item already exists in database: %s (%d)", item.Title, item.ItemYear.Int64))

		// Update fields if they differ
		updated := false

		if existingItem.Genres.String != item.Genres.String && item.Genres.Valid {
			existingItem.Genres = item.Genres
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating genres for item: %s", item.Title))
		}

		if existingItem.Rating.String != item.Rating.String && item.Rating.Valid {
			existingItem.Rating = item.Rating
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating rating for item: %s", item.Title))
		}

		if existingItem.Description.String != item.Description.String && item.Description.Valid {
			existingItem.Description = item.Description
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating description for item: %s", item.Title))
		}

		if existingItem.Category.String != item.Category.String && item.Category.Valid {
			existingItem.Category = item.Category
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating category for item: %s", item.Title))
		}

		if existingItem.Link.String != item.Link.String && item.Link.Valid {
			existingItem.Link = item.Link
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating link for item: %s", item.Title))
		}

		if existingItem.ThumbnailURL.String != item.ThumbnailURL.String && item.ThumbnailURL.Valid {
			existingItem.ThumbnailURL = item.ThumbnailURL
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating thumbnail URL for item: %s", item.Title))
		}

		// Also update IDs if we have new ones
		if item.ImdbID.Valid && !existingItem.ImdbID.Valid {
			existingItem.ImdbID = item.ImdbID
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating IMDB ID for item: %s", item.Title))
		}

		if item.TmdbID.Valid && !existingItem.TmdbID.Valid {
			existingItem.TmdbID = item.TmdbID
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating TMDB ID for item: %s", item.Title))
		}

		if item.TvdbID.Valid && !existingItem.TvdbID.Valid {
			existingItem.TvdbID = item.TvdbID
			updated = true
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Updating TVDB ID for item: %s", item.Title))
		}

		if updated {
			err = f.db.FetcherUpdateWatchlistItem(existingItem)
			if err != nil {
				f.log.Error("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Error updating item in database: %v", err))
				return
			}
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("Successfully updated item in database: %s (%d)", item.Title, item.ItemYear.Int64))
		} else {
			f.log.Info("PlexRSSFetcher", "processCustomParsedItem", fmt.Sprintf("No updates needed for item: %s (%d)", item.Title, item.ItemYear.Int64))
		}
	}
}

func extractTitleAndYear(fullTitle string) (string, sql.NullInt64) {
	re := regexp.MustCompile(`(.+) \((\d{4})\)`)
	match := re.FindStringSubmatch(fullTitle)

	if len(match) == 3 {
		year, _ := strconv.ParseInt(match[2], 10, 64)
		cleanTitle := strings.TrimSpace(match[1])
		return cleanTitle, sql.NullInt64{Int64: year, Valid: true}
	}

	return fullTitle, sql.NullInt64{Valid: false}
}

func extractIDs(guid string) (sql.NullString, sql.NullString, sql.NullString) {
	parts := strings.Split(guid, "://")
	if len(parts) != 2 {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}
	}

	switch parts[0] {
	case "imdb":
		return sql.NullString{String: parts[1], Valid: true}, sql.NullString{}, sql.NullString{}
	case "tmdb":
		return sql.NullString{}, sql.NullString{String: parts[1], Valid: true}, sql.NullString{}
	case "tvdb":
		return sql.NullString{}, sql.NullString{}, sql.NullString{String: parts[1], Valid: true}
	default:
		return sql.NullString{}, sql.NullString{}, sql.NullString{}
	}
}
