package indexers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"database/sql"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

const (
	APIURL = "https://api.themoviedb.org/3"
)

type TMDBIndexer struct {
	config      *config.Config
	db          *database.DB
	log         *logger.Logger
	client      *http.Client
	accessToken string
	baseURL     string
	cancel      context.CancelFunc
}

type ExternalIDs struct {
	IMDBID     string `json:"imdb_id"`
	TVDBID     int    `json:"tvdb_id"`
	WikidataID string `json:"wikidata_id"`
}

func NewTMDBIndexer(cfg *config.Config, db *database.DB, log *logger.Logger) *TMDBIndexer {
	// Configure HTTP client with optimized settings
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}

	return &TMDBIndexer{
		config:      cfg,
		db:          db,
		log:         log,
		client:      client,
		accessToken: cfg.TMDB.APIKey,
		baseURL:     APIURL,
	}
}

func (t *TMDBIndexer) makeRequest(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Add API key to URL if not already present
	if !strings.Contains(url, "api_key=") && !strings.Contains(url, "Authorization") {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url = fmt.Sprintf("%s%sapi_key=%s", url, separator, t.accessToken)
	}

	// Create sanitized URL for logging by removing the API key
	logURL := url
	if strings.Contains(logURL, "api_key=") {
		re := regexp.MustCompile(`api_key=[^&]+`)
		logURL = re.ReplaceAllString(logURL, "api_key=REDACTED")
	}
	t.log.Info("TMDBIndexer", "makeRequest", fmt.Sprintf("Making request to: %s", logURL))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+t.accessToken)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned non-200 status: %s, Body: %s", resp.Status, string(body))
	}

	return io.ReadAll(resp.Body)
}

func (t *TMDBIndexer) SearchMovies(query string, year int) ([]int, error) {
	searchURL := fmt.Sprintf("%s/search/movie?query=%s", t.baseURL, url.QueryEscape(query))
	if year > 0 {
		searchURL = fmt.Sprintf("%s&year=%d", searchURL, year)
	}

	t.log.Info("TMDBIndexer", "SearchMovies", fmt.Sprintf("Searching for movie: %s, year: %d", query, year))

	resp, err := t.makeRequest(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to search movies: %w", err)
	}

	var searchResult struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return nil, fmt.Errorf("failed to decode search movies response: %w", err)
	}

	var movieIDs []int
	for _, result := range searchResult.Results {
		movieIDs = append(movieIDs, result.ID)
	}

	return movieIDs, nil
}

func (t *TMDBIndexer) SearchTVShows(query string, year int) ([]int, error) {
	searchURL := fmt.Sprintf("%s/search/tv?query=%s", t.baseURL, url.QueryEscape(query))
	if year > 0 {
		searchURL = fmt.Sprintf("%s&first_air_date_year=%d", searchURL, year)
	}

	t.log.Info("TMDBIndexer", "SearchTVShows", fmt.Sprintf("Searching for TV show: %s, year: %d", query, year))

	resp, err := t.makeRequest(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to search TV shows: %w", err)
	}

	var searchResult struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return nil, fmt.Errorf("failed to decode search TV shows response: %w", err)
	}

	var tvIDs []int
	for _, result := range searchResult.Results {
		tvIDs = append(tvIDs, result.ID)
	}

	return tvIDs, nil
}

func (t *TMDBIndexer) Search(item *database.WatchlistItem) (*database.WatchlistItem, error) {
	t.log.Info("TMDBIndexer", "Search", fmt.Sprintf("Searching for item: %s", item.Title))

	// Extract year from title if present (e.g., "Movie Name (2020)")
	title := item.Title
	year := 0
	if re := regexp.MustCompile(`\((\d{4})\)`); re.MatchString(item.Title) {
		matches := re.FindStringSubmatch(item.Title)
		if len(matches) > 1 {
			if y, err := strconv.Atoi(matches[1]); err == nil {
				year = y
				title = strings.TrimSpace(re.ReplaceAllString(item.Title, ""))
			}
		}
	} else if item.ItemYear.Valid {
		year = int(item.ItemYear.Int64)
	}

	// Try TV shows first if we know it's a TV show
	if item.Category.String == "tv" {
		tvIDs, err := t.SearchTVShows(title, year)
		if err == nil && len(tvIDs) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(tvIDs[0]), Valid: true}
			return t.GetTVDetails(item)
		}
	}

	// Try movies if we know it's a movie or if TV show search failed
	if item.Category.String == "movie" || item.Category.String == "" {
		movieIDs, err := t.SearchMovies(title, year)
		if err == nil && len(movieIDs) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(movieIDs[0]), Valid: true}
			if err := t.GetMovieDetails(item); err == nil {
				return item, nil
			}
		}
	}

	// If we still haven't found anything and category is unknown, try TV shows
	if item.Category.String == "" {
		tvIDs, err := t.SearchTVShows(title, year)
		if err == nil && len(tvIDs) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(tvIDs[0]), Valid: true}
			return t.GetTVDetails(item)
		}
	}

	return nil, fmt.Errorf("no TMDB ID found for item '%s'", item.Title)
}

func (t *TMDBIndexer) Process(item *database.WatchlistItem) error {
	t.log.Info("TMDBIndexer", "Process", fmt.Sprintf("Processing item: %s", item.Title))

	// First try to search and get basic details
	updatedItem, err := t.Search(item)
	if err != nil {
		t.log.Warning("TMDBIndexer", "Process", fmt.Sprintf("Failed to search for item %s: %v", item.Title, err))
		return fmt.Errorf("failed to search for item: %w", err)
	}

	// If it's a TV show, get season and episode details
	if updatedItem.Category.String == "tv" {
		if err := t.GetSeasonDetails(updatedItem); err != nil {
			t.log.Warning("TMDBIndexer", "Process", fmt.Sprintf("Failed to get season details: %v", err))
			// Don't return error here as we already have basic show details
		}
	}

	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	item.Status = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		return fmt.Errorf("failed to update item: %w", err)
	}

	return nil
}

func (t *TMDBIndexer) GetMovieDetails(item *database.WatchlistItem) error {
	// If TMDB ID is not set, search for it
	if !item.TmdbID.Valid || item.TmdbID.String == "" {
		movieIDs, err := t.SearchMovies(item.Title, int(item.ItemYear.Int64))
		if err != nil || len(movieIDs) == 0 {
			//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
			item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
			if err := t.db.UpdateWatchlistItem(item); err != nil {
				t.log.Error("TMDBIndexer", "GetMovieDetails", fmt.Sprintf("Failed to update item status: %v", err))
			}
			return fmt.Errorf("no TMDB ID found for item '%s': %v", item.Title, err)
		}
		item.TmdbID = sql.NullString{String: strconv.Itoa(movieIDs[0]), Valid: true}
	}

	// Fetch movie details
	url := fmt.Sprintf("%s/movie/%s?language=en-US", t.baseURL, item.TmdbID.String)
	resp, err := t.makeRequest(url)
	if err != nil {
		//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
		item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
		if err := t.db.UpdateWatchlistItem(item); err != nil {
			t.log.Error("TMDBIndexer", "GetMovieDetails", fmt.Sprintf("Failed to update item status: %v", err))
		}
		return fmt.Errorf("failed to get movie details: %w", err)
	}

	var movieDetails struct {
		Title       string  `json:"title"`
		Overview    string  `json:"overview"`
		ReleaseDate string  `json:"release_date"`
		IMDBID      string  `json:"imdb_id"`
		VoteAverage float64 `json:"vote_average"`
		PosterPath  string  `json:"poster_path"`
		Status      string  `json:"status"`
	}

	if err := json.Unmarshal(resp, &movieDetails); err != nil {
		//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
		item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
		if err := t.db.UpdateWatchlistItem(item); err != nil {
			t.log.Error("TMDBIndexer", "GetMovieDetails", fmt.Sprintf("Failed to update item status: %v", err))
		}
		return fmt.Errorf("failed to decode movie details: %w", err)
	}

	// Update all fields
	item.Title = movieDetails.Title
	item.Description = sql.NullString{String: movieDetails.Overview, Valid: true}
	item.ImdbID = sql.NullString{String: movieDetails.IMDBID, Valid: true}
	item.ShowStatus = sql.NullString{String: movieDetails.Status, Valid: true}
	if movieDetails.PosterPath != "" {
		item.ThumbnailURL = sql.NullString{String: fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", movieDetails.PosterPath), Valid: true}
	}

	// Parse release date
	if movieDetails.ReleaseDate != "" {
		date, err := time.Parse("2006-01-02", movieDetails.ReleaseDate)
		if err == nil {
			item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
		}
	}

	// Set status to indexed and update
	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		t.log.Error("TMDBIndexer", "GetMovieDetails", fmt.Sprintf("Failed to update item: %v", err))
		return fmt.Errorf("failed to update item: %w", err)
	}

	return nil
}

func (t *TMDBIndexer) GetTVDetails(item *database.WatchlistItem) (*database.WatchlistItem, error) {
	// If TMDB ID is not set, search for it
	if !item.TmdbID.Valid || item.TmdbID.String == "" {
		tvIDs, err := t.SearchTVShows(item.Title, int(item.ItemYear.Int64))
		if err != nil || len(tvIDs) == 0 {
			item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
			if err := t.db.UpdateWatchlistItem(item); err != nil {
				t.log.Error("TMDBIndexer", "GetTVDetails", fmt.Sprintf("Failed to update item status: %v", err))
			}
			return nil, fmt.Errorf("no TMDB ID found for item '%s': %v", item.Title, err)
		}
		item.TmdbID = sql.NullString{String: strconv.Itoa(tvIDs[0]), Valid: true}
	}

	// Get show details from TMDB
	url := fmt.Sprintf("%s/tv/%s?language=en-US", t.baseURL, item.TmdbID.String)
	resp, err := t.makeRequest(url)
	if err != nil {
		item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
		if err := t.db.UpdateWatchlistItem(item); err != nil {
			t.log.Error("TMDBIndexer", "GetTVDetails", fmt.Sprintf("Failed to update item status: %v", err))
		}
		return nil, fmt.Errorf("failed to get show details: %w", err)
	}

	var showDetails struct {
		Name             string `json:"name"`
		Overview         string `json:"overview"`
		FirstAirDate     string `json:"first_air_date"`
		PosterPath       string `json:"poster_path"`
		Status           string `json:"status"`
		NumberOfSeasons  int    `json:"number_of_seasons"`
		NumberOfEpisodes int    `json:"number_of_episodes"`
		Genres           []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	}

	if err := json.Unmarshal(resp, &showDetails); err != nil {
		item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
		if err := t.db.UpdateWatchlistItem(item); err != nil {
			t.log.Error("TMDBIndexer", "GetTVDetails", fmt.Sprintf("Failed to update item status: %v", err))
		}
		return nil, fmt.Errorf("failed to parse show details: %w", err)
	}

	// Update item with show details
	item.Title = showDetails.Name
	item.Description = sql.NullString{String: showDetails.Overview, Valid: true}
	if showDetails.PosterPath != "" {
		item.ThumbnailURL = sql.NullString{String: fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", showDetails.PosterPath), Valid: true}
	}
	item.ShowStatus = sql.NullString{String: showDetails.Status, Valid: true}
	item.TotalSeasons = sql.NullInt32{Int32: int32(showDetails.NumberOfSeasons), Valid: true}
	item.TotalEpisodes = sql.NullInt32{Int32: int32(showDetails.NumberOfEpisodes), Valid: true}

	// Parse genres
	var genres []string
	for _, genre := range showDetails.Genres {
		genres = append(genres, strings.ToLower(genre.Name))
	}
	if len(genres) > 0 {
		item.Genres = sql.NullString{String: strings.Join(genres, ", "), Valid: true}
	}

	// Parse first air date
	if showDetails.FirstAirDate != "" {
		if date, err := time.Parse("2006-01-02", showDetails.FirstAirDate); err == nil {
			item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
		}
	}

	// Get external IDs
	externalIDs, err := t.GetExternalIDs(item.TmdbID.String)
	if err != nil {
		t.log.Warning("TMDBIndexer", "GetTVDetails", fmt.Sprintf("Failed to get external IDs: %v", err))
	} else {
		if externalIDs.IMDBID != "" {
			item.ImdbID = sql.NullString{String: externalIDs.IMDBID, Valid: true}
		}
		if externalIDs.TVDBID > 0 {
			item.TvdbID = sql.NullString{String: strconv.Itoa(externalIDs.TVDBID), Valid: true}
		}
	}

	// Set status to indexed and update
	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		return nil, fmt.Errorf("failed to update item: %w", err)
	}

	return item, nil
}

func (t *TMDBIndexer) updateMovieData(item *database.WatchlistItem) error {
	if !item.TmdbID.Valid {
		return fmt.Errorf("TMDB ID is missing")
	}

	// Get movie details from TMDB
	movieURL := fmt.Sprintf("%s/movie/%s?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	movieResponse, err := t.makeRequest(movieURL)
	if err != nil {
		return fmt.Errorf("failed to get movie details: %w", err)
	}

	var movieDetails struct {
		Title       string `json:"title"`
		ReleaseDate string `json:"release_date"`
		Overview    string `json:"overview"`
		PosterPath  string `json:"poster_path"`
		IMDbID      string `json:"imdb_id"`
		Status      string `json:"status"`
	}

	if err := json.Unmarshal(movieResponse, &movieDetails); err != nil {
		return fmt.Errorf("failed to parse movie details: %w", err)
	}

	// Update item with movie details
	item.Description = sql.NullString{String: movieDetails.Overview, Valid: true}
	if movieDetails.PosterPath != "" {
		item.ThumbnailURL = sql.NullString{String: fmt.Sprintf("https://image.tmdb.org/t/p/original%s", movieDetails.PosterPath), Valid: true}
	}
	if movieDetails.IMDbID != "" {
		item.ImdbID = sql.NullString{String: movieDetails.IMDbID, Valid: true}
	}

	// Parse and set release date
	if movieDetails.ReleaseDate != "" {
		releaseDate, err := time.Parse("2006-01-02", movieDetails.ReleaseDate)
		if err == nil {
			item.ReleaseDate = sql.NullTime{Time: releaseDate, Valid: true}
		}
	}

	// Get content ratings
	ratingsURL := fmt.Sprintf("%s/movie/%s/release_dates?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	ratingsResponse, err := t.makeRequest(ratingsURL)
	if err != nil {
		return fmt.Errorf("failed to get content ratings: %w", err)
	}

	var releaseDates struct {
		Results []struct {
			ISO31661     string `json:"iso_3166_1"`
			ReleaseDates []struct {
				Certification string `json:"certification"`
			} `json:"release_dates"`
		} `json:"results"`
	}

	if err := json.Unmarshal(ratingsResponse, &releaseDates); err != nil {
		return fmt.Errorf("failed to parse content ratings: %w", err)
	}

	// Try to find US rating first, then fall back to any rating
	rating := ""
	for _, r := range releaseDates.Results {
		if r.ISO31661 == "US" && len(r.ReleaseDates) > 0 {
			rating = r.ReleaseDates[0].Certification
			break
		}
	}
	if rating == "" && len(releaseDates.Results) > 0 && len(releaseDates.Results[0].ReleaseDates) > 0 {
		rating = releaseDates.Results[0].ReleaseDates[0].Certification
	}
	if rating != "" {
		item.Rating = sql.NullString{String: rating, Valid: true}
	}

	// Get genres
	genresURL := fmt.Sprintf("%s/movie/%s?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	genresResponse, err := t.makeRequest(genresURL)
	if err != nil {
		return fmt.Errorf("failed to get genres: %w", err)
	}

	var genreDetails struct {
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}

	if err := json.Unmarshal(genresResponse, &genreDetails); err != nil {
		return fmt.Errorf("failed to parse genres: %w", err)
	}

	var genres []string
	for _, g := range genreDetails.Genres {
		genres = append(genres, g.Name)
	}
	if len(genres) > 0 {
		item.Genres = sql.NullString{String: strings.Join(genres, ", "), Valid: true}
	}

	if movieDetails.Status != "" {
		item.ShowStatus = sql.NullString{String: movieDetails.Status, Valid: true}
	}

	return nil
}

func (t *TMDBIndexer) updateTVShowData(item *database.WatchlistItem) error {
	if !item.TmdbID.Valid {
		return fmt.Errorf("TMDB ID is missing")
	}

	// Correct URL for fetching TV show details
	url := fmt.Sprintf("%s/tv/%s?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	resp, err := t.makeRequest(url)
	if err != nil {
		return fmt.Errorf("failed to get show details: %w", err)
	}

	var showDetails struct {
		Name         string `json:"name"`
		Overview     string `json:"overview"`
		PosterPath   string `json:"poster_path"`
		FirstAirDate string `json:"first_air_date"`
		LastAirDate  string `json:"last_air_date"`
		Status       string `json:"status"`
		Seasons      []struct {
			SeasonNumber int    `json:"season_number"`
			EpisodeCount int    `json:"episode_count"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
		NumberOfSeasons  int `json:"number_of_seasons"`
		NumberOfEpisodes int `json:"number_of_episodes"`
	}

	if err := json.Unmarshal(resp, &showDetails); err != nil {
		return fmt.Errorf("failed to parse show details: %w", err)
	}

	// Update show details
	item.Description = sql.NullString{String: showDetails.Overview, Valid: true}
	if showDetails.PosterPath != "" {
		item.ThumbnailURL = sql.NullString{String: fmt.Sprintf("https://image.tmdb.org/t/p/original%s", showDetails.PosterPath), Valid: true}
	}
	item.TotalSeasons = sql.NullInt32{Int32: int32(showDetails.NumberOfSeasons), Valid: true}
	item.TotalEpisodes = sql.NullInt32{Int32: int32(showDetails.NumberOfEpisodes), Valid: true}
	item.ShowStatus = sql.NullString{String: showDetails.Status, Valid: true}

	// Parse and set release date
	if showDetails.FirstAirDate != "" {
		releaseDate, err := time.Parse("2006-01-02", showDetails.FirstAirDate)
		if err == nil {
			item.ReleaseDate = sql.NullTime{Time: releaseDate, Valid: true}
		}
	}

	// Get external IDs
	url = fmt.Sprintf("%s/tv/%s/external_ids?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	resp, err = t.makeRequest(url)
	if err != nil {
		return fmt.Errorf("failed to get external IDs: %w", err)
	}

	var externalIDs struct {
		IMDbID string `json:"imdb_id"`
		TVDbID int    `json:"tvdb_id"`
	}

	if err := json.Unmarshal(resp, &externalIDs); err != nil {
		return fmt.Errorf("failed to parse external IDs: %w", err)
	}

	if externalIDs.IMDbID != "" {
		item.ImdbID = sql.NullString{String: externalIDs.IMDbID, Valid: true}
	}
	if externalIDs.TVDbID != 0 {
		item.TvdbID = sql.NullString{String: strconv.Itoa(externalIDs.TVDbID), Valid: true}
	}

	// Get content ratings
	url = fmt.Sprintf("%s/tv/%s/content_ratings?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	resp, err = t.makeRequest(url)
	if err != nil {
		return fmt.Errorf("failed to get content ratings: %w", err)
	}

	var contentRatings struct {
		Results []struct {
			ISO31661 string `json:"iso_3166_1"`
			Rating   string `json:"rating"`
		} `json:"results"`
	}

	if err := json.Unmarshal(resp, &contentRatings); err != nil {
		return fmt.Errorf("failed to parse content ratings: %w", err)
	}

	// Try to find US rating first, then fall back to any rating
	rating := ""
	for _, r := range contentRatings.Results {
		if r.ISO31661 == "US" {
			rating = r.Rating
			break
		}
	}
	if rating == "" && len(contentRatings.Results) > 0 {
		rating = contentRatings.Results[0].Rating
	}
	if rating != "" {
		item.Rating = sql.NullString{String: rating, Valid: true}
	}

	// Get genres
	url = fmt.Sprintf("%s/tv/%s?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, t.accessToken)
	resp, err = t.makeRequest(url)
	if err != nil {
		return fmt.Errorf("failed to get genres: %w", err)
	}

	var genreDetails struct {
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}

	if err := json.Unmarshal(resp, &genreDetails); err != nil {
		return fmt.Errorf("failed to parse genres: %w", err)
	}

	var genres []string
	for _, g := range genreDetails.Genres {
		genres = append(genres, g.Name)
	}
	if len(genres) > 0 {
		item.Genres = sql.NullString{String: strings.Join(genres, ", "), Valid: true}
	}

	// Get and update season/episode details
	for _, season := range showDetails.Seasons {
		// Skip season 0 (usually specials)
		if season.SeasonNumber == 0 {
			continue
		}

		// Parse season air date
		var seasonAirDate time.Time
		if season.AirDate != "" {
			parsedDate, err := time.Parse("2006-01-02", season.AirDate)
			if err == nil {
				seasonAirDate = parsedDate
			} else {
				seasonAirDate = time.Now() // Use current time as fallback
			}
		} else {
			seasonAirDate = time.Now() // Use current time if no air date
		}

		// Insert or update season
		seasonID, err := t.db.InsertSeason(item.ID, season.SeasonNumber, season.EpisodeCount, seasonAirDate)
		if err != nil {
			t.log.Error("TMDBIndexer", "updateTVShowData", fmt.Sprintf("Failed to insert season %d: %v", season.SeasonNumber, err))
			continue
		}

		// Get episode details
		url = fmt.Sprintf("%s/tv/%s/season/%d?language=en-US&api_key=%s", t.baseURL, item.TmdbID.String, season.SeasonNumber, t.accessToken)
		resp, err = t.makeRequest(url)
		if err != nil {
			t.log.Error("TMDBIndexer", "updateTVShowData", fmt.Sprintf("Failed to get episode details for season %d: %v", season.SeasonNumber, err))
			continue
		}

		var seasonDetails struct {
			Episodes []struct {
				EpisodeNumber int    `json:"episode_number"`
				Name          string `json:"name"`
				AirDate       string `json:"air_date"`
				Overview      string `json:"overview"`
				StillPath     string `json:"still_path"`
			} `json:"episodes"`
		}

		if err := json.Unmarshal(resp, &seasonDetails); err != nil {
			t.log.Error("TMDBIndexer", "updateTVShowData", fmt.Sprintf("Failed to parse episode details for season %d: %v", season.SeasonNumber, err))
			continue
		}

		for _, episode := range seasonDetails.Episodes {
			// Convert episode air date to string
			episodeAirDateStr := ""
			if episode.AirDate != "" {
				episodeAirDateStr = episode.AirDate
			}

			// Insert or update episode
			err = t.db.InsertEpisode(seasonID, episode.EpisodeNumber, episode.Name, episodeAirDateStr)
			if err != nil {
				t.log.Error("TMDBIndexer", "updateTVShowData", fmt.Sprintf("Failed to insert episode %d: %v", episode.EpisodeNumber, err))
			}
		}
	}

	item.Status = sql.NullString{String: "indexed", Valid: true}
	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		return fmt.Errorf("failed to update item: %w", err)
	}

	return nil
}

// episodeNeedsUpdate checks if an episode needs to be updated by comparing its fields
func episodeNeedsUpdate(existing *database.TVEpisode, new *database.TVEpisode) bool {
	// Compare all relevant fields
	if existing.EpisodeName != new.EpisodeName {
		return true
	}
	if existing.AirDate.Valid != new.AirDate.Valid {
		return true
	}
	if existing.AirDate.Valid && new.AirDate.Valid && !existing.AirDate.Time.Equal(new.AirDate.Time) {
		return true
	}
	if existing.Overview != new.Overview {
		return true
	}
	if existing.StillPath != new.StillPath {
		return true
	}
	return false
}

type TVSeasonDetails struct {
	ID           int    `json:"id"`
	SeasonNumber int    `json:"season_number"`
	AirDate      string `json:"air_date"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	Episodes     []struct {
		AirDate       string `json:"air_date"`
		EpisodeNumber int    `json:"episode_number"`
		ID            int    `json:"id"`
		Name          string `json:"name"`
		StillPath     string `json:"still_path"`
		Overview      string `json:"overview"`
		SeasonNumber  int    `json:"season_number"`
	} `json:"episodes"`
}

type TMDBEpisode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	Overview      string `json:"overview"`
	StillPath     string `json:"still_path"`
}

func (t *TMDBIndexer) GetTVSeasonDetails(tvID string, seasonNumber int) (*TVSeasonDetails, error) {
	url := fmt.Sprintf("%s/tv/%s/season/%d?api_key=%s&language=en-US", APIURL, tvID, seasonNumber, t.accessToken)
	resp, err := t.makeRequest(url)
	if err != nil {
		return nil, err
	}

	var seasonDetails TVSeasonDetails
	err = json.Unmarshal(resp, &seasonDetails)
	if err != nil {
		return nil, err
	}

	return &seasonDetails, nil
}

func (t *TMDBIndexer) GetTVSeasonEpisodes(tvID string, seasonNumber int) ([]struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	Overview      string `json:"overview"`
	StillPath     string `json:"still_path"`
}, error) {
	seasonDetails, err := t.GetTVSeasonDetails(tvID, seasonNumber)
	if err != nil {
		return nil, err
	}

	var episodes []struct {
		EpisodeNumber int    `json:"episode_number"`
		Name          string `json:"name"`
		AirDate       string `json:"air_date"`
		Overview      string `json:"overview"`
		StillPath     string `json:"still_path"`
	}

	for _, episode := range seasonDetails.Episodes {
		episodes = append(episodes, struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			AirDate       string `json:"air_date"`
			Overview      string `json:"overview"`
			StillPath     string `json:"still_path"`
		}{
			EpisodeNumber: episode.EpisodeNumber,
			Name:          episode.Name,
			AirDate:       episode.AirDate,
			Overview:      episode.Overview,
			StillPath:     episode.StillPath,
		})
	}

	return episodes, nil
}

func (t *TMDBIndexer) FindByID(externalID string, source string) (*database.WatchlistItem, error) {
	findURL := fmt.Sprintf("%s/find/%s?api_key=%s&external_source=%s&language=en-US", APIURL, externalID, t.accessToken, source)

	resp, err := t.makeRequest(findURL)
	if err != nil {
		return nil, err
	}

	var result struct {
		MovieResults []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
			PosterPath  string `json:"poster_path"`
			Overview    string `json:"overview"`
			Genres      []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"genres"`
		} `json:"movie_results"`
		TVResults []struct {
			ID              int    `json:"id"`
			Name            string `json:"name"`
			FirstAirDate    string `json:"first_air_date"`
			PosterPath      string `json:"poster_path"`
			Overview        string `json:"overview"`
			NumberOfSeasons int    `json:"number_of_seasons"`
			Status          string `json:"status"`
			Genres          []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"genres"`
		} `json:"tv_results"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var item database.WatchlistItem

	if len(result.MovieResults) > 0 {
		movie := result.MovieResults[0]
		item.Title = movie.Title
		item.TmdbID = sql.NullString{String: fmt.Sprintf("%d", movie.ID), Valid: true}
		item.ThumbnailURL = sql.NullString{String: movie.PosterPath, Valid: true}
		item.Description = sql.NullString{String: movie.Overview, Valid: true}
		item.MediaType = sql.NullString{String: "movie", Valid: true}

		// Parse genres
		var genreNames []string
		for _, genre := range movie.Genres {
			genreNames = append(genreNames, strings.ToLower(genre.Name))
		}
		if len(genreNames) > 0 {
			item.Genres = sql.NullString{String: strings.Join(genreNames, ", "), Valid: true}
		}

		if movie.ReleaseDate != "" {
			if date, err := time.Parse("2006-01-02", movie.ReleaseDate); err == nil {
				item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
				item.ItemYear = sql.NullInt64{Int64: int64(date.Year()), Valid: true}
			}
		}

		// Get additional movie details if needed
		if err := t.updateMovieData(&item); err != nil {
			t.log.Warning("TMDBIndexer", "FindByID", fmt.Sprintf("Failed to get additional movie details: %v", err))
		}
	} else if len(result.TVResults) > 0 {
		show := result.TVResults[0]
		item.Title = show.Name
		item.TmdbID = sql.NullString{String: fmt.Sprintf("%d", show.ID), Valid: true}
		item.ThumbnailURL = sql.NullString{String: show.PosterPath, Valid: true}
		item.Description = sql.NullString{String: show.Overview, Valid: true}
		item.MediaType = sql.NullString{String: "tv", Valid: true}
		item.ShowStatus = sql.NullString{String: show.Status, Valid: true}
		item.TotalSeasons = sql.NullInt32{Int32: int32(show.NumberOfSeasons), Valid: true}

		// Parse genres
		var genreNames []string
		for _, genre := range show.Genres {
			genreNames = append(genreNames, strings.ToLower(genre.Name))
		}
		if len(genreNames) > 0 {
			item.Genres = sql.NullString{String: strings.Join(genreNames, ", "), Valid: true}
		}

		if show.FirstAirDate != "" {
			if date, err := time.Parse("2006-01-02", show.FirstAirDate); err == nil {
				item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
				item.ItemYear = sql.NullInt64{Int64: int64(date.Year()), Valid: true}
			}
		}

		// Get additional TV show details if needed
		if err := t.updateTVShowData(&item); err != nil {
			t.log.Warning("TMDBIndexer", "FindByID", fmt.Sprintf("Failed to get additional TV show details: %v", err))
		}
	} else {
		return nil, fmt.Errorf("no results found")
	}

	item.Status = sql.NullString{String: "indexed", Valid: true}
	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(&item); err != nil {
		return nil, fmt.Errorf("failed to update item: %w", err)
	}

	return &item, nil
}

func (t *TMDBIndexer) GetExternalIDs(tmdbID string) (*ExternalIDs, error) {
	t.log.Info("TMDBIndexer", "GetExternalIDs", fmt.Sprintf("Fetching external IDs for TMDB ID: %s", tmdbID))

	id, err := strconv.Atoi(tmdbID)
	if err != nil {
		return nil, fmt.Errorf("invalid TMDB ID: %v", err)
	}

	url := fmt.Sprintf("%s/tv/%d/external_ids?api_key=%s&language=en-US", t.baseURL, id, t.accessToken)
	resp, err := t.makeRequest(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch external IDs: %v", err)
	}

	t.log.Info("TMDBIndexer", "GetExternalIDs", fmt.Sprintf("Raw Response: %s", string(resp)))

	var externalIDs ExternalIDs
	if err := json.Unmarshal(resp, &externalIDs); err != nil {
		return nil, fmt.Errorf("failed to decode external IDs response: %v", err)
	}

	t.log.Info("TMDBIndexer", "GetExternalIDs", fmt.Sprintf("External IDs Response: %+v", externalIDs))
	return &externalIDs, nil
}

func (t *TMDBIndexer) GetSeasonDetails(item *database.WatchlistItem) error {
	if !item.TmdbID.Valid || item.TmdbID.String == "" {
		return fmt.Errorf("TMDB ID is required to get season details")
	}

	if !item.TotalSeasons.Valid {
		return fmt.Errorf("total seasons is required to get season details")
	}

	t.log.Info("TMDBIndexer", "GetSeasonDetails", fmt.Sprintf("Getting season details for show: %s", item.Title))

	url := fmt.Sprintf("%s/tv/%s?language=en-US", t.baseURL, item.TmdbID.String)
	resp, err := t.makeRequest(url)
	if err != nil {
		return fmt.Errorf("failed to get show details: %w", err)
	}

	var showDetails struct {
		Status           string `json:"status"`
		NumberOfSeasons  int    `json:"number_of_seasons"`
		NumberOfEpisodes int    `json:"number_of_episodes"`
	}

	if err := json.Unmarshal(resp, &showDetails); err != nil {
		return fmt.Errorf("failed to parse show details: %w", err)
	}

	item.ShowStatus = sql.NullString{String: showDetails.Status, Valid: true}
	item.TotalSeasons = sql.NullInt32{Int32: int32(showDetails.NumberOfSeasons), Valid: true}
	item.TotalEpisodes = sql.NullInt32{Int32: int32(showDetails.NumberOfEpisodes), Valid: true}

	if err := t.db.UpdateWatchlistItem(item); err != nil {
		return fmt.Errorf("failed to update watchlist item with show details: %w", err)
	}

	// Continue with fetching season details
	for season := 1; season <= int(item.TotalSeasons.Int32); season++ {
		url := fmt.Sprintf("%s/tv/%s/season/%d?language=en-US", t.baseURL, item.TmdbID.String, season)
		resp, err := t.makeRequest(url)
		if err != nil {
			t.log.Warning("TMDBIndexer", "GetSeasonDetails", fmt.Sprintf("Failed to get season %d details: %v", season, err))
			continue
		}

		var seasonDetails struct {
			SeasonNumber int `json:"season_number"`
			Episodes     []struct {
				EpisodeNumber int    `json:"episode_number"`
				Name          string `json:"name"`
				Overview      string `json:"overview"`
				AirDate       string `json:"air_date"`
				StillPath     string `json:"still_path"`
			} `json:"episodes"`
		}

		if err := json.Unmarshal(resp, &seasonDetails); err != nil {
			t.log.Warning("TMDBIndexer", "GetSeasonDetails", fmt.Sprintf("Failed to parse season %d details: %v", season, err))
			continue
		}

		// Save episode details to database
		for _, episode := range seasonDetails.Episodes {
			episodeItem := &database.Episode{
				ShowID:        item.ID,
				SeasonNumber:  season,
				EpisodeNumber: episode.EpisodeNumber,
				Title:         episode.Name,
				Description:   sql.NullString{String: episode.Overview, Valid: true},
				ThumbnailURL:  sql.NullString{String: fmt.Sprintf("https://image.tmdb.org/t/p/w500%s", episode.StillPath), Valid: episode.StillPath != ""},
			}

			if episode.AirDate != "" {
				if airDate, err := time.Parse("2006-01-02", episode.AirDate); err == nil {
					episodeItem.AirDate = sql.NullTime{Time: airDate, Valid: true}
				}
			}

			if err := t.db.SaveEpisode(episodeItem); err != nil {
				t.log.Warning("TMDBIndexer", "GetSeasonDetails", fmt.Sprintf("Failed to save episode S%dE%d: %v", season, episode.EpisodeNumber, err))
			}
		}
	}

	// Mark item as indexed only after successfully adding episodes
	//item.Status = sql.NullString{String: "indexed", Valid: true}
	item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		return fmt.Errorf("failed to mark item as indexed: %w", err)
	}

	return nil
}

func (t *TMDBIndexer) Start(ctx context.Context) error {
	t.log.Info("TMDBIndexer", "Start", "Starting TMDB indexer")

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				t.log.Info("TMDBIndexer", "Start", "Context cancelled, stopping TMDB indexer")
				return
			case <-ticker.C:
				if t.IsNeeded() {
					if err := t.UpdateExistingItems(); err != nil {
						t.log.Error("TMDBIndexer", "UpdateExistingItems", fmt.Sprintf("Error updating items: %v", err))
					}
				}
			}
		}
	}()

	return nil
}

func (t *TMDBIndexer) Stop() error {
	t.log.Info("TMDBIndexer", "Stop", "Stopping TMDB indexer")
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *TMDBIndexer) IsNeeded() bool {
	var count int
	err := t.db.QueryRow(`
		SELECT COUNT(*) 
		FROM watchlist 
		WHERE status = 'new' 
		AND (tmdb_id IS NULL OR tmdb_id = '')
	`).Scan(&count)

	return err == nil && count > 0
}

func (t *TMDBIndexer) Name() string {
	return "TMDBIndexer"
}

func (t *TMDBIndexer) UpdateExistingItems() error {
	items, err := t.db.GetAllWatchlistItems()
	if err != nil {
		return err
	}

	for _, item := range items {
		if item.CurrentStep.String == "new" || item.CurrentStep.String == "indexing" {
			item.CurrentStep = sql.NullString{String: "indexing", Valid: true}
			if err := t.db.UpdateWatchlistItem(&item); err != nil {
				t.log.Error("TMDBIndexer", "UpdateExistingItems", fmt.Sprintf("Failed to update item status: %v", err))
				return err
			}
		}

		if item.TmdbID.Valid && item.TmdbID.String != "" {
			updatedItem, err := t.UpdateItemWithTMDBData(&item)
			if err != nil {
				t.log.Error("TMDBIndexer", "UpdateExistingItems", fmt.Sprintf("Failed to update item %d (%s): %v", item.ID, item.Title, err))
			} else {
				t.log.Info("TMDBIndexer", "UpdateExistingItems", fmt.Sprintf("Successfully updated item %d (%s)", updatedItem.ID, updatedItem.Title))
			}
		}
	}

	return nil
}

func (t *TMDBIndexer) UpdateItemWithTMDBData(item *database.WatchlistItem) (*database.WatchlistItem, error) {
	t.log.Info("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Updating item: %s", item.Title))

	// If TMDB ID is missing, try to find it using external IDs
	if !item.TmdbID.Valid || item.TmdbID.String == "" {
		if err := t.findByExternalID(item); err != nil {
			// If external ID lookup fails, try searching by title
			t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("External ID lookup failed: %v, trying title search", err))
			updatedItem, err := t.Search(item)
			if err != nil {
				t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Title search failed: %v", err))
				item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
				if err := t.db.UpdateWatchlistItem(item); err != nil {
					t.log.Error("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update item status: %v", err))
				}
				return nil, fmt.Errorf("failed to find item: %w", err)
			}
			if updatedItem != nil {
				item = updatedItem
			}
		}
	}

	// Update the item with TMDB data based on its media type
	if item.MediaType.String == "movie" || item.Category.String == "movie" {
		item.MediaType = sql.NullString{String: "movie", Valid: true}
		if err := t.GetMovieDetails(item); err != nil {
			t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to get movie details: %v", err))
			//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
			item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
			if err := t.db.UpdateWatchlistItem(item); err != nil {
				t.log.Error("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update item status: %v", err))
			}
			return nil, fmt.Errorf("failed to get movie details: %w", err)
		}
	} else {
		item.MediaType = sql.NullString{String: "tv", Valid: true}
		updatedItem, err := t.GetTVDetails(item)
		if err != nil {
			t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to get TV details: %v", err))
			//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
			item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
			if err := t.db.UpdateWatchlistItem(item); err != nil {
				t.log.Error("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update item status: %v", err))
			}
			return nil, fmt.Errorf("failed to get TV details: %w", err)
		}
		item = updatedItem

		// Update seasons and episodes
		if err := t.updateTVShowData(item); err != nil {
			t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update TV show data: %v", err))
			//item.Status = sql.NullString{String: "indexing_failed", Valid: true}
			item.CurrentStep = sql.NullString{String: "indexing_failed", Valid: true}
			if err := t.db.UpdateWatchlistItem(item); err != nil {
				t.log.Error("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update item status: %v", err))
			}
		}
	}

	// Set final status (only if not already failed)
	if item.CurrentStep.String != "indexing_failed" {
		item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
	}

	// Final update to ensure all fields are saved
	if err := t.db.UpdateWatchlistItem(item); err != nil {
		t.log.Warning("TMDBIndexer", "UpdateItemWithTMDBData", fmt.Sprintf("Failed to update item: %v", err))
		return nil, fmt.Errorf("failed to update item: %w", err)
	}

	return item, nil
}

func (t *TMDBIndexer) findByExternalID(item *database.WatchlistItem) error {
	// Try IMDB ID first for movies
	if item.ImdbID.Valid && item.ImdbID.String != "" {
		url := fmt.Sprintf("%s/find/%s?external_source=imdb_id&api_key=%s&language=en-US", t.baseURL, item.ImdbID.String, t.accessToken)
		resp, err := t.makeRequest(url)
		if err != nil {
			return fmt.Errorf("failed to find by IMDB ID: %w", err)
		}

		var findResult struct {
			MovieResults []struct {
				ID        int    `json:"id"`
				Title     string `json:"title"`
				MediaType string `json:"media_type"`
			} `json:"movie_results"`
			TVResults []struct {
				ID        int    `json:"id"`
				Name      string `json:"name"`
				MediaType string `json:"media_type"`
			} `json:"tv_results"`
		}

		if err := json.Unmarshal(resp, &findResult); err != nil {
			return fmt.Errorf("failed to parse find results: %w", err)
		}

		// For movies, check movie results first
		if (item.Category.String == "movie" || item.MediaType.String == "movie") && len(findResult.MovieResults) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(findResult.MovieResults[0].ID), Valid: true}
			item.MediaType = sql.NullString{String: "movie", Valid: true}
			return nil
		}

		// For TV shows or if movie wasn't found, check TV results
		if len(findResult.TVResults) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(findResult.TVResults[0].ID), Valid: true}
			item.MediaType = sql.NullString{String: "tv", Valid: true}
			return nil
		}
	}

	// Try TVDB ID for TV shows
	if item.TvdbID.Valid && item.TvdbID.String != "" {
		url := fmt.Sprintf("%s/find/%s?external_source=tvdb_id&api_key=%s&language=en-US", t.baseURL, item.TvdbID.String, t.accessToken)
		resp, err := t.makeRequest(url)
		if err != nil {
			return fmt.Errorf("failed to find by TVDB ID: %w", err)
		}

		var findResult struct {
			TVResults []struct {
				ID        int    `json:"id"`
				Name      string `json:"name"`
				MediaType string `json:"media_type"`
			} `json:"tv_results"`
		}

		if err := json.Unmarshal(resp, &findResult); err != nil {
			return fmt.Errorf("failed to parse find results: %w", err)
		}

		if len(findResult.TVResults) > 0 {
			item.TmdbID = sql.NullString{String: strconv.Itoa(findResult.TVResults[0].ID), Valid: true}
			item.MediaType = sql.NullString{String: "tv", Valid: true}
			return nil
		}
	}

	return fmt.Errorf("no results found with external IDs")
}

func (t *TMDBIndexer) SearchMulti(query string) ([]*database.WatchlistItem, error) {
	url := fmt.Sprintf("%s/search/multi?query=%s&language=en-US&page=1", APIURL, url.QueryEscape(query))
	resp, err := t.makeRequest(url)
	if err != nil {
		t.log.Error("TMDBIndexer", "SearchMulti", fmt.Sprintf("Failed to search: %v", err))
		return nil, err
	}

	var searchResult struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			Overview     string  `json:"overview"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			PosterPath   string  `json:"poster_path"`
			MediaType    string  `json:"media_type"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}

	if err := json.Unmarshal(resp, &searchResult); err != nil {
		t.log.Error("TMDBIndexer", "SearchMulti", fmt.Sprintf("Failed to decode search results: %v", err))
		return nil, err
	}

	var items []*database.WatchlistItem
	for _, result := range searchResult.Results {
		if result.MediaType != "movie" && result.MediaType != "tv" {
			continue
		}

		item := &database.WatchlistItem{
			TmdbID:      sql.NullString{String: fmt.Sprintf("%d", result.ID), Valid: true},
			Category:    sql.NullString{String: result.MediaType, Valid: true},
			Rating:      sql.NullString{String: fmt.Sprintf("%.1f", result.VoteAverage), Valid: true},
			Description: sql.NullString{String: result.Overview, Valid: true},
			ThumbnailURL: sql.NullString{
				String: func() string {
					if result.PosterPath != "" {
						return "https://image.tmdb.org/t/p/w500" + result.PosterPath
					}
					return ""
				}(),
				Valid: true,
			},
			MediaType: sql.NullString{String: result.MediaType, Valid: true},
		}

		if result.MediaType == "movie" {
			item.Title = result.Title
			if result.ReleaseDate != "" {
				if date, err := time.Parse("2006-01-02", result.ReleaseDate); err == nil {
					item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
					item.ItemYear = sql.NullInt64{Int64: int64(date.Year()), Valid: true}
				}
			}
		} else {
			item.Title = result.Name
			if result.FirstAirDate != "" {
				if date, err := time.Parse("2006-01-02", result.FirstAirDate); err == nil {
					item.ReleaseDate = sql.NullTime{Time: date, Valid: true}
					item.ItemYear = sql.NullInt64{Int64: int64(date.Year()), Valid: true}
				}
			}
		}

		item.Status = sql.NullString{String: "indexed", Valid: true}
		item.CurrentStep = sql.NullString{String: "indexed", Valid: true}
		if err := t.db.UpdateWatchlistItem(item); err != nil {
			t.log.Error("TMDBIndexer", "SearchMulti", fmt.Sprintf("Failed to update item: %v", err))
		}

		items = append(items, item)
	}

	return items, nil
}
