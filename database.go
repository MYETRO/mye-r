package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
)

// DB struct represents the database connection
type DB struct {
	*sql.DB
}

// WatchlistItem represents a single watchlist item.
type WatchlistItem struct {
	ID                    int            `json:"id"`
	Title                 string         `json:"title"`
	ItemYear              sql.NullInt64  `json:"item_year"`
	RequestedDate         time.Time      `json:"requested_date"`
	Link                  sql.NullString `json:"link"`
	ImdbID                sql.NullString `json:"imdb_id"`
	TmdbID                sql.NullString `json:"tmdb_id"`
	TvdbID                sql.NullString `json:"tvdb_id"`
	Description           sql.NullString `json:"description"`
	Category              sql.NullString `json:"category"`
	Genres                sql.NullString `json:"genres"`
	Rating                sql.NullString `json:"rating"`
	Status                sql.NullString `json:"status"`
	CurrentStep           sql.NullString `json:"current_step"`
	ThumbnailURL          sql.NullString `json:"thumbnail_url"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	BestScrapedFilename   sql.NullString `json:"best_scraped_filename"`
	BestScrapedResolution sql.NullString `json:"best_scraped_resolution"`
	LastScrapedDate       sql.NullTime   `json:"last_scraped_date"`
	CustomLibrary         sql.NullString `json:"custom_library"`
	MainLibraryPath       sql.NullString `json:"main_library_path"`
	BestScrapedScore      sql.NullInt32  `json:"best_scraped_score"`
	MediaType             sql.NullString `json:"media_type"`
	TotalSeasons          sql.NullInt32  `json:"total_seasons"`
	TotalEpisodes         sql.NullInt32  `json:"total_episodes"`
	ReleaseDate           sql.NullTime   `json:"release_date"`
	ShowStatus            sql.NullString `json:"show_status"`
	RetryCount            sql.NullInt32  `json:"retry_count"`
}

// NewDB creates a new database connection
func NewDB(dataSourceName string) (*DB, error) {
	db, err := sql.Open("postgres", dataSourceName)
	if err != nil {
		return nil, err
	}

	// Set connection pool settings
	db.SetMaxOpenConns(25)                  // Maximum number of open connections
	db.SetMaxIdleConns(5)                   // Maximum number of idle connections
	db.SetConnMaxLifetime(time.Hour)        // Maximum lifetime of a connection
	db.SetConnMaxIdleTime(30 * time.Minute) // Maximum idle time for a connection

	if err = db.Ping(); err != nil {
		return nil, err
	}
	return &DB{db}, nil
}

// GetWatchlistItem retrieves a single watchlist item by ID
func (db *DB) GetWatchlistItem(id int) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE id = $1
	`
	var item WatchlistItem
	err := db.QueryRow(query, id).Scan(
		&item.ID,
		&item.Title,
		&item.ItemYear,
		&item.RequestedDate,
		&item.Link,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.Description,
		&item.Category,
		&item.Genres,
		&item.Rating,
		&item.Status,
		&item.CurrentStep,
		&item.ThumbnailURL,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.BestScrapedFilename,
		&item.BestScrapedResolution,
		&item.LastScrapedDate,
		&item.CustomLibrary,
		&item.MainLibraryPath,
		&item.BestScrapedScore,
		&item.MediaType,
		&item.TotalSeasons,
		&item.TotalEpisodes,
		&item.ReleaseDate,
		&item.ShowStatus,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("watchlist item not found")
		}
		return nil, fmt.Errorf("error getting watchlist item: %v", err)
	}
	return &item, nil
}

// GetInfoHashForItem retrieves the info_hash for a given item from the scrape_results table
func (db *DB) GetInfoHashForItem(itemID int) (string, error) {
	query := `
		SELECT info_hash
		FROM scrape_results
		WHERE watchlist_item_id = $1
		ORDER BY scraped_score DESC
		LIMIT 1
	`
	var infoHash string
	err := db.QueryRow(query, itemID).Scan(&infoHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no scrape results found for item")
		}
		return "", fmt.Errorf("error getting info_hash: %v", err)
	}
	return infoHash, nil
}

// CreateWatchlistItem inserts a new watchlist item into the database
func (db *DB) CreateWatchlistItem(item *WatchlistItem) error {
	query := `
		INSERT INTO watchlistitem (
			title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			description, category, genres, rating, status, current_step, thumbnail_url,
			created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			last_scraped_date, custom_library, main_library_path, best_scraped_score,
			media_type, total_seasons, total_episodes, release_date, show_status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
		RETURNING id
	`

	err := db.QueryRow(
		query,
		item.Title, item.ItemYear, item.RequestedDate, item.Link,
		item.ImdbID, item.TmdbID, item.TvdbID, item.Description,
		item.Category, item.Genres, item.Rating, item.Status,
		item.CurrentStep, item.ThumbnailURL, item.CreatedAt, item.UpdatedAt,
		item.BestScrapedFilename, item.BestScrapedResolution, item.LastScrapedDate,
		item.CustomLibrary, item.MainLibraryPath, item.BestScrapedScore,
		item.MediaType, item.TotalSeasons, item.TotalEpisodes, item.ReleaseDate, item.ShowStatus,
	).Scan(&item.ID)

	if err != nil {
		return fmt.Errorf("failed to create watchlist item: %v", err)
	}

	return nil
}

// FetcherUpdateWatchlistItem updates an existing watchlist item in the database
func (db *DB) FetcherUpdateWatchlistItem(item *WatchlistItem) error {
	query := `
		UPDATE watchlistitem SET
			title = $2, item_year = $3, requested_date = $4, link = $5,
			imdb_id = $6, tmdb_id = $7, tvdb_id = $8, description = $9,
			category = $10, genres = $11, rating = $12, status = $13,
			current_step = $14, thumbnail_url = $15, updated_at = $16, 
			best_scraped_filename = $17, best_scraped_resolution = $18, last_scraped_date = $19, 
			custom_library = $20, main_library_path = $21, 
			best_scraped_score = $22, release_date = $23, media_type = $24,
			total_seasons = $25, total_episodes = $26, show_status = $27
		WHERE id = $1
	`

	_, err := db.Exec(query,
		item.ID, item.Title, item.ItemYear, item.RequestedDate, item.Link,
		item.ImdbID, item.TmdbID, item.TvdbID, item.Description,
		item.Category, item.Genres, item.Rating, item.Status,
		item.CurrentStep, item.ThumbnailURL, time.Now(), item.BestScrapedFilename,
		item.BestScrapedResolution, item.LastScrapedDate, item.CustomLibrary,
		item.MainLibraryPath, item.BestScrapedScore, item.ReleaseDate, item.MediaType,
		item.TotalSeasons, item.TotalEpisodes, item.ShowStatus,
	)

	if err != nil {
		return fmt.Errorf("failed to update watchlist item: %v", err)
	}

	return nil
}

// UpdateWatchlistItem updates an existing watchlist item in the database
func (db *DB) UpdateWatchlistItem(item *WatchlistItem) error {
	query := `UPDATE watchlistitem SET 
		tmdb_id = CASE WHEN $1 = '' THEN NULL ELSE $1 END, 
		title = $2, 
		description = $3, 
		release_date = $4, 
		rating = $5, 
		thumbnail_url = $6, 
		media_type = $7, 
		total_seasons = $8, 
		total_episodes = $9,
		show_status = $10,
		status = $11,
		current_step = $12,
		imdb_id = CASE WHEN $13 = '' THEN NULL ELSE $13 END,
		tvdb_id = CASE WHEN $14 = '' THEN NULL ELSE $14 END,
		updated_at = $15
		WHERE id = $16`

	_, err := db.Exec(query,
		item.TmdbID.String,
		item.Title,
		item.Description.String,
		item.ReleaseDate.Time,
		item.Rating.String,
		item.ThumbnailURL.String,
		item.MediaType.String,
		item.TotalSeasons.Int32,
		item.TotalEpisodes.Int32,
		item.ShowStatus.String,
		item.Status.String,
		item.CurrentStep.String,
		item.ImdbID.String,
		item.TvdbID.String,
		time.Now(),
		item.ID)

	return err
}

// GetNextItemForScraping retrieves the next item from the watchlist that needs scraping
func (db *DB) GetNextItemForScraping() (*WatchlistItem, error) {
	query := `
		WITH ReleasedEpisodes AS (
			SELECT DISTINCT season.watchlist_item_id
			FROM tv_episode episode
			JOIN season ON episode.season_id = season.id
			WHERE episode.air_date <= NOW()
		)
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem w
		WHERE (status = 'new' OR status = 'scrape_failed')
		AND (
			(media_type = 'movie' AND (release_date IS NULL OR release_date <= NOW()))
			OR 
			(media_type = 'tv' AND w.id IN (SELECT watchlist_item_id FROM ReleasedEpisodes))
		)
		ORDER BY id ASC
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error getting next item for scraping: %v", err)
	}
	return &item, nil
}

// GetNextItemForLibraryMatching retrieves the next item from the watchlist that needs library matching
func (db *DB) GetNextItemForLibraryMatching() (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE status IN ('new', 'ready_for_matching')
		ORDER BY requested_date ASC
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil // No items available
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get next item for library matching: %v", err)
	}
	return &item, nil
}

// GetNextItemForSymlinking retrieves the next item from the watchlist that needs symlinking
func (db *DB) GetNextItemForSymlinking() (*WatchlistItem, error) {
	query := `
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
	`
	var item WatchlistItem
	err := db.QueryRow(query).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.ReleaseDate, &item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error getting next item for symlinking: %v", err)
	}
	return &item, nil
}

// GetAllWatchlistItems retrieves all watchlist items from the database
func (db *DB) GetAllWatchlistItems() ([]WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		ORDER BY id ASC
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("error getting all watchlist items: %v", err)
	}
	defer rows.Close()

	var items []WatchlistItem
	for rows.Next() {
		var item WatchlistItem
		err := rows.Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
			&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
			&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
			&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
			&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning watchlist item: %v", err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating watchlist items: %v", err)
	}
	return items, nil
}

// FindWatchlistItemByIDs searches for a watchlist item by its various IDs
func (db *DB) FindWatchlistItemByIDs(imdbID, tmdbID, tvdbID string) (*WatchlistItem, error) {
	log.Printf("DEBUG: Searching for item with IDs - IMDB: %s, TMDB: %s, TVDB: %s", imdbID, tmdbID, tvdbID)

	// Try exact match first (all provided IDs must match)
	exactQuery := `SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id, 
		description, category, genres, rating, status, current_step, thumbnail_url, created_at, 
		updated_at, best_scraped_filename, best_scraped_resolution, last_scraped_date, custom_library, 
		main_library_path, best_scraped_score, media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem 
		WHERE 
		($1 = '' OR imdb_id = $1) AND 
		($2 = '' OR tmdb_id = $2) AND 
		($3 = '' OR tvdb_id = $3)
		LIMIT 1`

	var item WatchlistItem
	err := db.QueryRow(exactQuery, imdbID, tmdbID, tvdbID).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
	)

	if err != nil && err != sql.ErrNoRows {
		log.Printf("ERROR: Failed to find item by IDs: %v", err)
		return nil, err
	}

	if err == nil {
		log.Printf("DEBUG: Found exact match for item: %s (%v) with ID %d", item.Title, item.ItemYear.Int64, item.ID)
		return &item, nil
	}

	// If no exact match found and we have an IMDB ID, try finding by IMDB ID only
	if imdbID != "" {
		imdbQuery := `SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id, 
			description, category, genres, rating, status, current_step, thumbnail_url, created_at, 
			updated_at, best_scraped_filename, best_scraped_resolution, last_scraped_date, custom_library, 
			main_library_path, best_scraped_score, media_type, total_seasons, total_episodes, release_date, show_status
			FROM watchlistitem 
			WHERE imdb_id = $1
			LIMIT 1`

		err = db.QueryRow(imdbQuery, imdbID).Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
			&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
			&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
			&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
			&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
		)

		if err == nil {
			log.Printf("DEBUG: Found item by IMDB ID: %s (%v) with ID %d", item.Title, item.ItemYear.Int64, item.ID)
			return &item, nil
		}
	}

	// If still no match and we have a TMDB ID, try finding by TMDB ID only
	if tmdbID != "" {
		tmdbQuery := `SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id, 
			description, category, genres, rating, status, current_step, thumbnail_url, created_at, 
			updated_at, best_scraped_filename, best_scraped_resolution, last_scraped_date, custom_library, 
			main_library_path, best_scraped_score, media_type, total_seasons, total_episodes, release_date, show_status
			FROM watchlistitem 
			WHERE tmdb_id = $1
			LIMIT 1`

		err = db.QueryRow(tmdbQuery, tmdbID).Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
			&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
			&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
			&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
			&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
		)

		if err == nil {
			log.Printf("DEBUG: Found item by TMDB ID: %s (%v) with ID %d", item.Title, item.ItemYear.Int64, item.ID)
			return &item, nil
		}
	}

	log.Printf("DEBUG: No item found with these IDs")
	return nil, nil
}

// UpdateWatchlistItemForLibraryMatching updates the library matching related fields of a watchlist item
func (db *DB) UpdateWatchlistItemForLibraryMatching(item *WatchlistItem) error {
	query := `
		UPDATE watchlistitem
		SET custom_library = $1, status = $2, main_library_path = $3, current_step = $4, updated_at = NOW()
		WHERE id = $5
	`
	_, err := db.Exec(query, item.CustomLibrary, item.Status, item.MainLibraryPath, item.CurrentStep, item.ID)
	if err != nil {
		return fmt.Errorf("failed to update watchlist item: %v", err)
	}
	return nil
}

// InsertTVEpisode inserts a new TV episode into the database
func (db *DB) InsertTVEpisode(episode *TVEpisode) error {
	query := `
		INSERT INTO tv_episodes (season_id, episode_number, episode_name, air_date, overview, still_path)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`

	var airDate sql.NullTime
	if episode.AirDate.Valid {
		airDate = episode.AirDate
	}

	return db.QueryRow(query,
		episode.SeasonID,
		episode.EpisodeNumber,
		episode.EpisodeName,
		airDate,
		episode.Overview,
		episode.StillPath,
	).Scan(&episode.ID)
}

// TVEpisode represents a single TV episode
type TVEpisode struct {
	ID             int            `json:"id"`
	SeasonID       int            `json:"season_id"`
	EpisodeNumber  int            `json:"episode_number"`
	EpisodeName    sql.NullString `json:"episode_name"`
	AirDate        sql.NullTime   `json:"air_date"`
	Overview       sql.NullString `json:"overview"`
	StillPath      sql.NullString `json:"still_path"`
	Scraped        bool           `json:"scraped"`
	ScrapeResultID sql.NullInt32  `json:"scrape_result_id"`
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.DB.Close()
}

// CreateScrapeResult inserts a new scrape result into the database
func (db *DB) CreateScrapeResult(result *ScrapeResult) error {
	query := `
		INSERT INTO scrape_results (
			watchlist_item_id, scraped_filename, scraped_resolution,
			scraped_date, info_hash, scraped_score, scraped_file_size,
			scraped_codec, status_results
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	err := db.QueryRow(
		query,
		result.WatchlistItemID,
		result.ScrapedFilename,
		result.ScrapedResolution,
		result.ScrapedDate,
		result.InfoHash,
		result.ScrapedScore,
		result.ScrapedFileSize,
		result.ScrapedCodec,
		result.StatusResults,
	).Scan(&result.ID, &result.CreatedAt, &result.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create scrape result: %v", err)
	}

	return nil
}

func (db *DB) GetItemStatus(itemID int) (string, error) {
	query := `
		SELECT status_results
		FROM scrape_results
		WHERE watchlist_item_id = $1
		ORDER BY scraped_date DESC
		LIMIT 1
	`
	var status sql.NullString
	err := db.QueryRow(query, itemID).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("failed to get item status: %v", err)
	}
	if !status.Valid {
		return "", fmt.Errorf("status not found for item %d", itemID)
	}
	return status.String, nil
}

func (db *DB) GetNextItemForDownload() (*WatchlistItem, error) {
	query := `
		SELECT w.id, w.title, w.item_year, w.requested_date, w.link, w.imdb_id, w.tmdb_id, w.tvdb_id,
			   w.description, w.category, w.genres, w.rating, w.status, w.current_step, w.thumbnail_url,
			   w.created_at, w.updated_at, w.best_scraped_filename, w.best_scraped_resolution,
			   w.last_scraped_date, w.custom_library, w.main_library_path, w.best_scraped_score,
			   w.media_type, w.total_seasons, w.total_episodes, w.release_date, w.show_status
		FROM watchlistitem w
		LEFT JOIN scrape_results s ON s.watchlist_item_id = w.id
		WHERE 
			(
				w.current_step = 'scraped'
				OR s.status_results = 'scraped'
			)
		AND (
			-- Movies are eligible as long as conditions are met
			w.media_type = 'movie'
			OR
			-- TV shows must have all episodes scraped
			(
				w.media_type = 'tv'
				AND NOT EXISTS (
					SELECT 1 
					FROM tv_episodes 
					WHERE watchlist_item_id = w.id
					AND scraped = false
				)
			)
		)
		ORDER BY w.requested_date ASC
		LIMIT 1
	`
	var item WatchlistItem
	var scrapeStatus sql.NullString // To capture scrape_results.status_results
	err := db.QueryRow(query).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
		&scrapeStatus, // Add this to capture scrape_results.status_results
	)

	if err == sql.ErrNoRows {
		return nil, nil // No items available
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get next item for download: %v", err)
	}
	return &item, nil
}

func (db *DB) GetWatchlistItemByID(itemID int) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE id = $1
	`
	var item WatchlistItem
	err := db.QueryRow(query, itemID).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil // No item found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get item by ID: %v", err)
	}
	return &item, nil
}

func (db *DB) InsertWatchlistItem(item *WatchlistItem) error {
	query := `
		INSERT INTO watchlistitem (
			title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			description, category, genres, rating, status, current_step,
			thumbnail_url, created_at, updated_at, show_status
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
		)
	`
	_, err := db.Exec(query,
		item.Title,
		item.ItemYear,
		item.RequestedDate,
		item.Link,
		item.ImdbID,
		item.TmdbID,
		item.TvdbID,
		item.Description,
		item.Category,
		item.Genres,
		item.Rating,
		item.Status,
		item.CurrentStep,
		item.ThumbnailURL,
		time.Now(), // CreatedAt
		time.Now(), // UpdatedAt
		item.ShowStatus,
	)
	return err
}

// FindWatchlistItemByTitleAndYear retrieves a watchlist item by title and year
func (db *DB) FindWatchlistItemByTitleAndYear(title string, year int64) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE title = $1 AND item_year = $2
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query, title, year).Scan(
		&item.ID,
		&item.Title,
		&item.ItemYear,
		&item.RequestedDate,
		&item.Link,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.Description,
		&item.Category,
		&item.Genres,
		&item.Rating,
		&item.Status,
		&item.CurrentStep,
		&item.ThumbnailURL,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.BestScrapedFilename,
		&item.BestScrapedResolution,
		&item.LastScrapedDate,
		&item.CustomLibrary,
		&item.MainLibraryPath,
		&item.BestScrapedScore,
		&item.MediaType,
		&item.TotalSeasons,
		&item.TotalEpisodes,
		&item.ReleaseDate,
		&item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// DeleteWatchlistItemByTitleAndYear removes an item from the database by title and year
// and also removes any items with matching IDs to ensure complete cleanup
func (db *DB) DeleteWatchlistItemByTitleAndYear(title string, year int64) error {
	// First, get the item to find its ID
	item, err := db.FindWatchlistItemByTitleAndYear(title, year)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("item not found")
	}

	// Delete scrape results first
	err = db.DeleteScrapeResultsForItem(item.ID)
	if err != nil {
		return fmt.Errorf("failed to delete scrape results: %v", err)
	}

	// Now delete the watchlist item
	query := `DELETE FROM watchlistitem WHERE title = $1 AND item_year = $2`
	_, err = db.Exec(query, title, year)
	if err != nil {
		return err
	}

	return nil
}

func (db *DB) InsertSeason(watchlistItemID int, seasonNumber int, episodeCount int, airDate time.Time) (int, error) {
	var seasonID int
	// Check if the season already exists
	query := `SELECT id FROM seasons WHERE watchlist_item_id = $1 AND season_number = $2`
	err := db.QueryRow(query, watchlistItemID, seasonNumber).Scan(&seasonID)

	if err == sql.ErrNoRows {
		// If no existing season, insert a new one
		query = `INSERT INTO seasons (watchlist_item_id, season_number, episode_count, air_date) 
				 VALUES ($1, $2, $3, $4) RETURNING id`
		err = db.QueryRow(query, watchlistItemID, seasonNumber, episodeCount, airDate).Scan(&seasonID)
		if err != nil {
			return 0, fmt.Errorf("failed to insert season: %v", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("failed to check for existing season: %v", err)
	} else {
		// If the season exists, update it
		query = `UPDATE seasons SET episode_count = $1, air_date = $2 WHERE id = $3`
		_, err = db.Exec(query, episodeCount, airDate, seasonID)
		if err != nil {
			return 0, fmt.Errorf("failed to update existing season: %v", err)
		}
	}

	return seasonID, nil
}

func (db *DB) InsertEpisode(seasonID int, episodeNumber int, episodeName string, airDate string) error {
	// Check if the episode already exists
	var existingEpisodeID int
	query := `SELECT id FROM tv_episodes WHERE season_id = $1 AND episode_number = $2`
	err := db.QueryRow(query, seasonID, episodeNumber).Scan(&existingEpisodeID)

	// Parse the air date string to a time.Time
	var airDateTime sql.NullTime
	if airDate != "" {
		if parsedTime, err := time.Parse("2006-01-02", airDate); err == nil {
			airDateTime = sql.NullTime{Time: parsedTime, Valid: true}
		}
	}

	if err == sql.ErrNoRows {
		// If no existing episode, insert a new one
		query = `INSERT INTO tv_episodes (season_id, episode_number, episode_name, air_date) 
				  VALUES ($1, $2, $3, $4)`
		_, err = db.Exec(query, seasonID, episodeNumber, episodeName, airDateTime)
		return err
	} else if err != nil {
		return fmt.Errorf("failed to check for existing episode: %v", err)
	} else {
		// If the episode exists, update it
		query = `UPDATE tv_episodes SET episode_name = $1, air_date = $2 WHERE id = $3`
		_, err = db.Exec(query, episodeName, airDateTime, existingEpisodeID)
		return err
	}
}

// UpdateWatchlistItemIDs updates the IMDb, TMDB, and TVDB IDs of an existing watchlist item in the database
func (db *DB) UpdateWatchlistItemIDs(item *WatchlistItem) error {
	query := `
		UPDATE watchlistitem SET
			imdb_id = $1,
			tmdb_id = $2,
			tvdb_id = $3,
			updated_at = $4
		WHERE id = $5
	`

	_, err := db.Exec(query,
		item.ImdbID,
		item.TmdbID,
		item.TvdbID,
		time.Now(), // UpdatedAt
		item.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update watchlist item IDs: %v", err)
	}

	return nil
}

// UpdateExternalIDs updates the IMDb, TMDB, and TVDB IDs of an existing watchlist item in the database
func (db *DB) UpdateExternalIDs(item *WatchlistItem) error {
	query := `
		UPDATE watchlistitem SET
			imdb_id = $1,
			tmdb_id = $2,
			tvdb_id = $3,
			updated_at = $4
		WHERE id = $5
	`

	_, err := db.Exec(query,
		item.ImdbID,
		item.TmdbID,
		item.TvdbID,
		time.Now(), // UpdatedAt
		item.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update external IDs: %v", err)
	}

	return nil
}

// Season represents a TV show season
type Season struct {
	ID              int            `json:"id"`
	WatchlistItemID int            `json:"watchlist_item_id"`
	SeasonNumber    int            `json:"season_number"`
	AirDate         sql.NullTime   `json:"air_date"`
	Overview        sql.NullString `json:"overview"`
	PosterPath      sql.NullString `json:"poster_path"`
	EpisodeCount    sql.NullInt32  `json:"episode_count"`
}

// GetSeasonsForItem retrieves all seasons for a given watchlist item
func (db *DB) GetSeasonsForItem(watchlistItemID int) ([]*Season, error) {
	query := `
		SELECT id, watchlist_item_id, season_number, air_date, overview, poster_path, episode_count
		FROM seasons
		WHERE watchlist_item_id = $1
		ORDER BY season_number ASC
	`
	rows, err := db.Query(query, watchlistItemID)
	if err != nil {
		return nil, fmt.Errorf("error getting seasons: %v", err)
	}
	defer rows.Close()

	var seasons []*Season
	for rows.Next() {
		var season Season
		err := rows.Scan(
			&season.ID,
			&season.WatchlistItemID,
			&season.SeasonNumber,
			&season.AirDate,
			&season.Overview,
			&season.PosterPath,
			&season.EpisodeCount,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning season: %v", err)
		}
		seasons = append(seasons, &season)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating seasons: %v", err)
	}
	return seasons, nil
}

// GetEpisodesForSeason retrieves all episodes for a given season
func (db *DB) GetEpisodesForSeason(seasonID int) ([]TVEpisode, error) {
	query := `
		SELECT id, season_id, episode_number, episode_name, air_date, overview, still_path, scraped, scrape_result_id
		FROM tv_episodes
		WHERE season_id = $1
		ORDER BY episode_number ASC
	`
	rows, err := db.Query(query, seasonID)
	if err != nil {
		return nil, fmt.Errorf("error getting episodes: %v", err)
	}
	defer rows.Close()

	var episodes []TVEpisode
	for rows.Next() {
		var episode TVEpisode
		err := rows.Scan(
			&episode.ID,
			&episode.SeasonID,
			&episode.EpisodeNumber,
			&episode.EpisodeName,
			&episode.AirDate,
			&episode.Overview,
			&episode.StillPath,
			&episode.Scraped,
			&episode.ScrapeResultID,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning episode: %v", err)
		}
		episodes = append(episodes, episode)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating episodes: %v", err)
	}
	return episodes, nil
}

// UpdateTVEpisode updates an existing TV episode in the database
func (db *DB) UpdateTVEpisode(episode *TVEpisode) error {
	query := `
		UPDATE tv_episodes
		SET episode_name = $1,
			air_date = $2,
			overview = $3,
			still_path = $4,
			scraped = $5,
			scrape_result_id = $6
		WHERE id = $7
	`
	_, err := db.Exec(query,
		episode.EpisodeName,
		episode.AirDate,
		episode.Overview,
		episode.StillPath,
		episode.Scraped,
		episode.ScrapeResultID,
		episode.ID,
	)
	if err != nil {
		return fmt.Errorf("error updating episode: %v", err)
	}
	return nil
}

// SaveScrapeResult saves a scrape result to the database and returns its ID
func (db *DB) SaveScrapeResult(result *ScrapeResult) (int, error) {
	// First check if we already have a result for this item with the same info hash
	if result.InfoHash.Valid {
		var existingID int
		err := db.QueryRow(`
			SELECT id FROM scrape_results 
			WHERE watchlist_item_id = $1 AND info_hash = $2
		`, result.WatchlistItemID, result.InfoHash).Scan(&existingID)

		if err == nil {
			// Update the existing result instead of creating a new one
			result.ID = existingID
			if err := db.UpdateScrapeResult(result); err != nil {
				return 0, fmt.Errorf("failed to update existing scrape result: %v", err)
			}
			return existingID, nil
		} else if err != sql.ErrNoRows {
			return 0, fmt.Errorf("error checking for existing scrape result: %v", err)
		}
	}

	// No existing result found, insert a new one
	query := `
		INSERT INTO scrape_results (
			watchlist_item_id, scraped_filename, scraped_resolution, 
			scraped_date, info_hash, scraped_score, scraped_file_size, 
			scraped_codec, status_results, debrid_id, debrid_uri,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		) RETURNING id`

	var id int
	err := db.QueryRow(
		query,
		result.WatchlistItemID,
		result.ScrapedFilename,
		result.ScrapedResolution,
		result.ScrapedDate,
		result.InfoHash,
		result.ScrapedScore,
		result.ScrapedFileSize,
		result.ScrapedCodec,
		result.StatusResults,
		result.DebridID,
		result.DebridURI,
		time.Now(),
		time.Now(),
	).Scan(&id)

	if err != nil {
		return 0, fmt.Errorf("failed to save scrape result: %v", err)
	}

	return id, nil
}

// GetItemsForTMDB returns a list of item IDs that need TMDB metadata. Items are selected if:
// 1. They have status = 'new'
func (db *DB) GetItemsForTMDB() ([]int, error) {
	query := `
		SELECT DISTINCT id 
		FROM watchlistitem
		WHERE status = 'new'
		ORDER BY id ASC
	`
	return db.getItemIDs(query)
}

// GetItemsForScraper returns a list of item IDs that need scraping
func (db *DB) GetItemsForScraper() ([]int, error) {
	query := `
		SELECT id
		FROM watchlistitem
		WHERE (best_scraped_score IS NULL OR best_scraped_score < 100)
		AND status != 'completed'
		AND tmdb_id IS NOT NULL
		ORDER BY created_at DESC
	`
	return db.getItemIDs(query)
}

// GetItemsForDownloader returns a list of item IDs that need downloading
func (db *DB) GetItemsForDownloader() ([]int, error) {
	query := `
		SELECT DISTINCT w.id
		FROM watchlistitem w
		INNER JOIN scrape_results sr ON w.id = sr.watchlist_item_id
		WHERE w.status != 'completed'
		AND sr.info_hash IS NOT NULL
		AND sr.downloaded = false
		AND sr.status_results = 'scraped'
		AND w.media_type = 'tv'
		ORDER BY w.created_at DESC
	`
	return db.getItemIDs(query)
}

// GetItemsForLibraryMatcher returns a list of item IDs that need library matching
func (db *DB) GetItemsForLibraryMatcher() ([]int, error) {
	query := `
		SELECT id
		FROM watchlistitem
		WHERE (custom_library IS NULL OR main_library_path IS NULL)
		AND status != 'completed'
		AND tmdb_id IS NOT NULL
		ORDER BY created_at DESC
	`
	return db.getItemIDs(query)
}

// getItemIDs is a helper function to execute a query and return a list of item IDs
func (db *DB) getItemIDs(query string) ([]int, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("error querying items: %v", err)
	}
	defer rows.Close()

	var itemIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("error scanning item ID: %v", err)
		}
		itemIDs = append(itemIDs, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	return itemIDs, nil
}

// UpdateSeason updates an existing season in the database
func (db *DB) UpdateSeason(season *Season) error {
	query := `
		UPDATE seasons
		SET air_date = $1,
			overview = $2,
			poster_path = $3,
			episode_count = $4
		WHERE id = $5
	`
	_, err := db.Exec(query,
		season.AirDate,
		season.Overview,
		season.PosterPath,
		season.EpisodeCount,
		season.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update season: %v", err)
	}
	return nil
}

// DeleteScrapeResultsForItem deletes all scrape results for a given watchlist item
func (db *DB) DeleteScrapeResultsForItem(itemID int) error {
	query := `DELETE FROM scrape_results WHERE watchlist_item_id = $1`
	_, err := db.Exec(query, itemID)
	return err
}

// FindWatchlistItemByIMDBID finds a watchlist item by IMDB ID
func (db *DB) FindWatchlistItemByIMDBID(imdbID string) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE imdb_id = $1
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query, imdbID).Scan(
		&item.ID,
		&item.Title,
		&item.ItemYear,
		&item.RequestedDate,
		&item.Link,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.Description,
		&item.Category,
		&item.Genres,
		&item.Rating,
		&item.Status,
		&item.CurrentStep,
		&item.ThumbnailURL,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.BestScrapedFilename,
		&item.BestScrapedResolution,
		&item.LastScrapedDate,
		&item.CustomLibrary,
		&item.MainLibraryPath,
		&item.BestScrapedScore,
		&item.MediaType,
		&item.TotalSeasons,
		&item.TotalEpisodes,
		&item.ReleaseDate,
		&item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// FindWatchlistItemByTMDBID finds a watchlist item by TMDB ID
func (db *DB) FindWatchlistItemByTMDBID(tmdbID string) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE tmdb_id = $1
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query, tmdbID).Scan(
		&item.ID,
		&item.Title,
		&item.ItemYear,
		&item.RequestedDate,
		&item.Link,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.Description,
		&item.Category,
		&item.Genres,
		&item.Rating,
		&item.Status,
		&item.CurrentStep,
		&item.ThumbnailURL,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.BestScrapedFilename,
		&item.BestScrapedResolution,
		&item.LastScrapedDate,
		&item.CustomLibrary,
		&item.MainLibraryPath,
		&item.BestScrapedScore,
		&item.MediaType,
		&item.TotalSeasons,
		&item.TotalEpisodes,
		&item.ReleaseDate,
		&item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// FindWatchlistItemByTVDBID finds a watchlist item by TVDB ID
func (db *DB) FindWatchlistItemByTVDBID(tvdbID string) (*WatchlistItem, error) {
	query := `
		SELECT id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			   description, category, genres, rating, status, current_step, thumbnail_url,
			   created_at, updated_at, best_scraped_filename, best_scraped_resolution,
			   last_scraped_date, custom_library, main_library_path, best_scraped_score,
			   media_type, total_seasons, total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE tvdb_id = $1
		LIMIT 1
	`
	var item WatchlistItem
	err := db.QueryRow(query, tvdbID).Scan(
		&item.ID,
		&item.Title,
		&item.ItemYear,
		&item.RequestedDate,
		&item.Link,
		&item.ImdbID,
		&item.TmdbID,
		&item.TvdbID,
		&item.Description,
		&item.Category,
		&item.Genres,
		&item.Rating,
		&item.Status,
		&item.CurrentStep,
		&item.ThumbnailURL,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.BestScrapedFilename,
		&item.BestScrapedResolution,
		&item.LastScrapedDate,
		&item.CustomLibrary,
		&item.MainLibraryPath,
		&item.BestScrapedScore,
		&item.MediaType,
		&item.TotalSeasons,
		&item.TotalEpisodes,
		&item.ReleaseDate,
		&item.ShowStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// Episode represents a single TV episode
type Episode struct {
	ID            int            `json:"id"`
	ShowID        int            `json:"show_id"`
	SeasonNumber  int            `json:"season_number"`
	EpisodeNumber int            `json:"episode_number"`
	Title         string         `json:"title"`
	Description   sql.NullString `json:"description"`
	AirDate       sql.NullTime   `json:"air_date"`
	ThumbnailURL  sql.NullString `json:"thumbnail_url"`
}

// SaveEpisode saves an episode to the database
func (db *DB) SaveEpisode(episode *Episode) error {
	query := `
		INSERT INTO episodes (show_id, season_number, episode_number, title, description, air_date, thumbnail_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (show_id, season_number, episode_number)
		DO UPDATE SET
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			air_date = EXCLUDED.air_date,
			thumbnail_url = EXCLUDED.thumbnail_url
		RETURNING id`

	return db.QueryRow(query,
		episode.ShowID,
		episode.SeasonNumber,
		episode.EpisodeNumber,
		episode.Title,
		episode.Description,
		episode.AirDate,
		episode.ThumbnailURL,
	).Scan(&episode.ID)
}

// GetItemsByStatus retrieves all items with a specific status
func (db *DB) GetItemsByStatus(status string) ([]*WatchlistItem, error) {
	query := `
		SELECT 
			id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			description, category, genres, rating, status, thumbnail_url, created_at,
			updated_at, best_scraped_filename, best_scraped_resolution, last_scraped_date,
			custom_library, main_library_path, best_scraped_score, media_type, total_seasons,
			total_episodes, release_date, retry_count, show_status, current_step
		FROM watchlistitem 
		WHERE status = $1::character varying(20) OR current_step = $1::character varying(200)
		ORDER BY id`

	rows, err := db.Query(query, status)
	if err != nil {
		return nil, fmt.Errorf("error querying items by status: %v", err)
	}
	defer rows.Close()

	var items []*WatchlistItem
	for rows.Next() {
		item := &WatchlistItem{}
		err := rows.Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
			&item.Genres, &item.Rating, &item.Status, &item.ThumbnailURL, &item.CreatedAt,
			&item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
			&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath,
			&item.BestScrapedScore, &item.MediaType, &item.TotalSeasons, &item.TotalEpisodes,
			&item.ReleaseDate, &item.RetryCount, &item.ShowStatus, &item.CurrentStep,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning item: %v", err)
		}
		items = append(items, item)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %v", err)
	}

	return items, nil
}

// GetItemsByStatusAndStep retrieves all items with a specific status and current_step
// GetItemsByStatusAndStep retrieves all items with three current_step conditions and a status_results condition
func (db *DB) GetItemsByStatusAndStep(currentStep1, currentStep2, currentStep3, statusResults string) ([]*WatchlistItem, error) {
	query := `
		SELECT 
			wi.id, wi.title, wi.item_year, wi.requested_date, wi.link, wi.imdb_id, wi.tmdb_id, wi.tvdb_id,
			wi.description, wi.category, wi.genres, wi.rating, wi.current_step, wi.thumbnail_url,
			wi.created_at, wi.updated_at, wi.best_scraped_filename, wi.best_scraped_resolution,
			wi.last_scraped_date, wi.custom_library, wi.main_library_path, wi.best_scraped_score,
			wi.media_type, wi.total_seasons, wi.total_episodes, wi.release_date, wi.retry_count, wi.show_status
		FROM watchlistitem wi
		LEFT JOIN scrape_results sr ON sr.watchlist_item_id = wi.id
		WHERE wi.current_step IN ($1, $2, $3)
		OR (sr.status_results = $4 OR sr.status_results IS NULL) -- Handle NULL case
		ORDER BY wi.requested_date ASC;

	`

	// Prepare a slice to hold the results
	var items []*WatchlistItem

	// Execute the query with four parameters: currentStep1, currentStep2, currentStep3, and statusResults
	rows, err := db.Query(query, currentStep1, currentStep2, currentStep3, statusResults)
	if err != nil {
		return nil, fmt.Errorf("error getting items by status and step: %v", err)
	}
	defer rows.Close()

	// Loop over the result set and scan into the items slice
	for rows.Next() {
		var item WatchlistItem
		if err := rows.Scan(&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link, &item.ImdbID,
			&item.TmdbID, &item.TvdbID, &item.Description, &item.Category, &item.Genres, &item.Rating,
			&item.CurrentStep, &item.ThumbnailURL, &item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename,
			&item.BestScrapedResolution, &item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath,
			&item.BestScrapedScore, &item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate,
			&item.RetryCount, &item.ShowStatus); err != nil {
			return nil, fmt.Errorf("error scanning rows: %v", err)
		}
		items = append(items, &item)
	}

	// Check for any error encountered while iterating over rows
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error with rows iteration: %v", err)
	}

	return items, nil
}

// GetReturningSeriesWithUnscrapedEpisodes gets TV shows that are marked as "Returning Series"
// and have episodes that haven't been scraped yet and have an air date before now
func (db *DB) GetReturningSeriesWithUnscrapedEpisodes() ([]*WatchlistItem, error) {
	query := `
		SELECT DISTINCT w.id, w.title, w.item_year, w.requested_date, w.link, w.imdb_id, 
			   w.tmdb_id, w.tvdb_id, w.description, w.category, w.genres, w.rating, 
			   w.status, w.current_step, w.thumbnail_url, w.created_at, w.updated_at, 
			   w.best_scraped_filename, w.best_scraped_resolution, w.last_scraped_date, 
			   w.custom_library, w.main_library_path, w.best_scraped_score, w.media_type, 
			   w.total_seasons, w.total_episodes, w.release_date, w.show_status
		FROM watchlistitem w
		JOIN seasons s ON s.watchlist_item_id = w.id
		JOIN tv_episodes e ON e.season_id = s.id
		WHERE w.show_status = 'Returning Series'
		AND e.air_date <= CURRENT_DATE
		AND e.scraped = false
		ORDER BY w.id ASC
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("error querying returning series: %v", err)
	}
	defer rows.Close()

	var items []*WatchlistItem
	for rows.Next() {
		item := &WatchlistItem{}
		err := rows.Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
			&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
			&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
			&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
			&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate, &item.ShowStatus,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning returning series: %v", err)
		}
		item.MediaType = sql.NullString{String: "tv", Valid: true}
		items = append(items, item)
	}

	return items, nil
}

// GetScrapeResultsForItem retrieves all scrape results for an item that need processing
func (db *DB) GetScrapeResultsForItem(itemID int) ([]*ScrapeResult, error) {
	query := `
		SELECT sr.id, sr.watchlist_item_id, sr.info_hash, sr.scraped_filename, sr.scraped_file_size, 
			   sr.scraped_resolution, sr.scraped_score, sr.scraped_codec, sr.status_results, sr.created_at, sr.updated_at,
			   sr.debrid_id, sr.debrid_uri, sr.downloaded
		FROM scrape_results sr
		WHERE sr.watchlist_item_id = $1
		AND sr.status_results = 'scraped'
		ORDER BY sr.scraped_score DESC
	`

	rows, err := db.Query(query, itemID)
	if err != nil {
		return nil, fmt.Errorf("error querying scrape results: %v", err)
	}
	defer rows.Close()

	var results []*ScrapeResult
	for rows.Next() {
		var result ScrapeResult
		err := rows.Scan(
			&result.ID,
			&result.WatchlistItemID,
			&result.InfoHash,
			&result.ScrapedFilename,
			&result.ScrapedFileSize,
			&result.ScrapedResolution,
			&result.ScrapedScore,
			&result.ScrapedCodec,
			&result.StatusResults,
			&result.CreatedAt,
			&result.UpdatedAt,
			&result.DebridID,
			&result.DebridURI,
			&result.Downloaded,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning scrape result: %v", err)
		}
		results = append(results, &result)
	}

	return results, nil
}

// GetUnprocessedEpisodes gets episodes that have been released but not symlinked
func (db *DB) GetUnprocessedEpisodes(itemID int) ([]*TVEpisode, error) {
	query := `
		SELECT e.* 
		FROM tv_episodes e
		JOIN seasons s ON e.season_id = s.id
		WHERE s.watchlist_item_id = $1
		AND e.air_date <= NOW()
		AND (e.scraped = false OR EXISTS (
			SELECT 1 FROM scrape_results sr 
			WHERE sr.episode_id = e.id 
			AND sr.status_results 'scraped' -- IN ('scraped', 'downloaded', 'hash_ignored') -- Adjusted based on previous updates
		))
		ORDER BY e.season_id, e.episode_number
	`

	rows, err := db.Query(query, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to query unprocessed episodes: %v", err)
	}
	defer rows.Close()

	var episodes []*TVEpisode
	for rows.Next() {
		episode := &TVEpisode{}
		err := rows.Scan(
			&episode.ID,
			&episode.SeasonID,
			&episode.EpisodeNumber,
			&episode.EpisodeName,
			&episode.AirDate,
			&episode.Overview,
			&episode.StillPath,
			&episode.Scraped,
			&episode.ScrapeResultID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan episode: %v", err)
		}
		episodes = append(episodes, episode)
	}

	return episodes, nil
}

// GetScrapeResultsByEpisode gets all scrape results for a specific episode
func (db *DB) GetScrapeResultsByEpisode(episodeID int) ([]*ScrapeResult, error) {
	query := `
		SELECT sr.id, sr.watchlist_item_id, sr.scraped_filename, sr.scraped_resolution,
			   sr.scraped_date, sr.info_hash, sr.scraped_score, sr.scraped_file_size,
			   sr.scraped_codec, sr.status_results, sr.debrid_id, sr.debrid_uri,
			   sr.created_at, sr.updated_at
		FROM scrape_results sr
		WHERE sr.id = (
			SELECT scrape_result_id 
			FROM tv_episodes 
			WHERE id = $1
		)
	`
	rows, err := db.Query(query, episodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query scrape results: %v", err)
	}
	defer rows.Close()

	var results []*ScrapeResult
	for rows.Next() {
		result := &ScrapeResult{}
		err := rows.Scan(
			&result.ID,
			&result.WatchlistItemID,
			&result.ScrapedFilename,
			&result.ScrapedResolution,
			&result.ScrapedDate,
			&result.InfoHash,
			&result.ScrapedScore,
			&result.ScrapedFileSize,
			&result.ScrapedCodec,
			&result.StatusResults,
			&result.DebridID,
			&result.DebridURI,
			&result.CreatedAt,
			&result.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan scrape result: %v", err)
		}
		results = append(results, result)
	}

	return results, nil
}

// GetItemsWithSymlinks returns a batch of items that have symlinked scrape results
func (db *DB) GetItemsWithSymlinks(limit int, offset int) ([]*WatchlistItem, error) {
	var items []*WatchlistItem
	query := `
		SELECT DISTINCT wi.* 
		FROM watchlistitem wi 
		JOIN scraperesult sr ON sr.watchlist_item_id = wi.id 
		WHERE sr.status_results = 'symlinked'
		LIMIT $1 OFFSET $2
	`
	rows, err := db.Query(query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to get items with symlinks: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item WatchlistItem
		err := rows.Scan(
			&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate,
			&item.Link, &item.ImdbID, &item.TmdbID, &item.TvdbID,
			&item.Description, &item.Category, &item.Genres, &item.Rating,
			&item.Status, &item.CurrentStep, &item.ThumbnailURL,
			&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename,
			&item.BestScrapedResolution, &item.LastScrapedDate,
			&item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
			&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes,
			&item.ReleaseDate, &item.ShowStatus, &item.RetryCount,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning watchlist item: %v", err)
		}
		items = append(items, &item)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	return items, nil
}

// UpdateItemStatus updates the status and current_step of a watchlist item
func (db *DB) UpdateItemStatus(itemID int64, status string, currentStep string) error {
	query := `
		UPDATE watchlistitem 
		SET status = $1, current_step = $2, updated_at = NOW()
		WHERE id = $3
	`
	result, err := db.Exec(query, status, currentStep, itemID)
	if err != nil {
		return fmt.Errorf("failed to update item status: %v", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error getting rows affected: %v", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no item found with ID %d", itemID)
	}

	return nil
}

// GetTVEpisodesForItem retrieves all TV episodes for a given watchlist item
func (db *DB) GetTVEpisodesForItem(itemID int) ([]TVEpisode, error) {
	query := `
		SELECT e.id, e.season_id, e.episode_number, e.episode_name, e.air_date, e.overview, e.still_path, e.scraped, e.scrape_result_id
		FROM tv_episodes e
		JOIN seasons s ON e.season_id = s.id
		WHERE s.watchlist_item_id = $1
		ORDER BY e.season_id, e.episode_number
	`

	rows, err := db.Query(query, itemID)
	if err != nil {
		return nil, fmt.Errorf("error querying TV episodes: %v", err)
	}
	defer rows.Close()

	var episodes []TVEpisode
	for rows.Next() {
		var episode TVEpisode
		err := rows.Scan(
			&episode.ID,
			&episode.SeasonID,
			&episode.EpisodeNumber,
			&episode.EpisodeName,
			&episode.AirDate,
			&episode.Overview,
			&episode.StillPath,
			&episode.Scraped,
			&episode.ScrapeResultID,
		)
		if err != nil {
			return nil, fmt.Errorf("error scanning TV episode: %v", err)
		}
		episodes = append(episodes, episode)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating TV episodes: %v", err)
	}

	return episodes, nil
}
