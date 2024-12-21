package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/symlinker"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

// TestDB extends the database.DB struct to override GetNextItemForSymlinking
type TestDB struct {
	*database.DB
}

// GetNextItemForSymlinking overrides the original method to only return item 85
func (db *TestDB) GetNextItemForSymlinking() (*database.WatchlistItem, error) {
	query := `
		SELECT w.id, w.title, w.item_year, w.requested_date, w.link, w.imdb_id, w.tmdb_id, w.tvdb_id,
			   w.description, w.category, w.genres, w.rating, w.status, w.current_step::text, w.thumbnail_url,
			   w.created_at, w.updated_at, w.best_scraped_filename, w.best_scraped_resolution,
			   w.last_scraped_date, w.custom_library, w.main_library_path, w.best_scraped_score,
			   w.media_type, w.total_seasons, w.total_episodes, w.release_date
		FROM watchlistitem w
		WHERE w.id = 85
		AND w.status = 'downloaded'
		LIMIT 1
	`
	var item database.WatchlistItem
	err := db.QueryRow(query).Scan(
		&item.ID, &item.Title, &item.ItemYear, &item.RequestedDate, &item.Link,
		&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.Description, &item.Category,
		&item.Genres, &item.Rating, &item.Status, &item.CurrentStep, &item.ThumbnailURL,
		&item.CreatedAt, &item.UpdatedAt, &item.BestScrapedFilename, &item.BestScrapedResolution,
		&item.LastScrapedDate, &item.CustomLibrary, &item.MainLibraryPath, &item.BestScrapedScore,
		&item.MediaType, &item.TotalSeasons, &item.TotalEpisodes, &item.ReleaseDate,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error getting next item for symlinking: %v", err)
	}
	return &item, nil
}

// QueryRow implements the DBInterface
func (db *TestDB) QueryRow(query string, args ...interface{}) *sql.Row {
	return db.DB.QueryRow(query, args...)
}

// GetLatestScrapeResult implements the DBInterface
func (db *TestDB) GetLatestScrapeResult(itemID int) (*database.ScrapeResult, error) {
	return db.DB.GetLatestScrapeResult(itemID)
}

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		fmt.Println("Warning: .env file not found")
	}

	// Load configuration
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		fmt.Printf("Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize database connection
	baseDB, err := database.NewDB(cfg.Database.URL)
	if err != nil {
		fmt.Printf("Failed to initialize database: %v\n", err)
		os.Exit(1)
	}
	defer baseDB.Close()

	// Create test DB wrapper
	db := &TestDB{baseDB}

	// Update item 85 to be ready for symlinking
	_, err = db.Exec(`
		UPDATE watchlistitem 
		SET status = 'downloaded', current_step = 'symlink_pending'
		WHERE id = 85
	`)
	if err != nil {
		fmt.Printf("Failed to update item status: %v\n", err)
		os.Exit(1)
	}

	// Create symlinker with our test DB
	sl := symlinker.New(cfg, db)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Start symlinker
	if err := sl.Start(ctx); err != nil {
		fmt.Printf("Failed to start symlinker: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Symlinker started. Press Ctrl+C to stop...")
	<-ctx.Done()
}
