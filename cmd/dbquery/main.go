package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"
	_ "github.com/lib/pq"
)

func main() {
	db, err := sql.Open("postgres", "postgresql://postgres:postgres@10.18.149.71:5433/plex_watchlist?sslmode=disable")
	if err != nil {
		log.Fatal("Error connecting to database:", err)
	}
	defer db.Close()

	var (
		id                    int
		title                 string
		itemYear             sql.NullInt64
		requestedDate        time.Time
		link                 sql.NullString
		imdbID               sql.NullString
		tmdbID               sql.NullString
		tvdbID               sql.NullString
		description          sql.NullString
		category             sql.NullString
		genres               sql.NullString
		rating               sql.NullString
		status               sql.NullString
		currentStep          sql.NullInt64
		thumbnailURL         sql.NullString
		createdAt            time.Time
		updatedAt            time.Time
		mediaType            sql.NullString
		totalSeasons         sql.NullInt32
		totalEpisodes        sql.NullInt32
		releaseDate          sql.NullTime
		showStatus           sql.NullString
	)

	err = db.QueryRow(`
		SELECT 
			id, title, item_year, requested_date, link, imdb_id, tmdb_id, tvdb_id,
			description, category, genres, rating, status, current_step,
			thumbnail_url, created_at, updated_at, media_type, total_seasons,
			total_episodes, release_date, show_status
		FROM watchlistitem
		WHERE id = 664
	`).Scan(
		&id, &title, &itemYear, &requestedDate, &link, &imdbID, &tmdbID, &tvdbID,
		&description, &category, &genres, &rating, &status, &currentStep,
		&thumbnailURL, &createdAt, &updatedAt, &mediaType, &totalSeasons,
		&totalEpisodes, &releaseDate, &showStatus,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Println("No item found with ID 664")
			return
		}
		log.Fatal("Error querying database:", err)
	}

	fmt.Printf("ID: %d\n", id)
	fmt.Printf("Title: %s\n", title)
	fmt.Printf("Year: %d\n", itemYear.Int64)
	fmt.Printf("Status: %s\n", status.String)
	fmt.Printf("Media Type: %s\n", mediaType.String)
	fmt.Printf("TMDB ID: %s\n", tmdbID.String)
	fmt.Printf("IMDB ID: %s\n", imdbID.String)
	fmt.Printf("Description: %s\n", description.String)
	if mediaType.String == "show" {
		fmt.Printf("Total Seasons: %d\n", totalSeasons.Int32)
		fmt.Printf("Total Episodes: %d\n", totalEpisodes.Int32)
		fmt.Printf("Show Status: %s\n", showStatus.String)
	}
}
