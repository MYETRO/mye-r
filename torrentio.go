package scraper

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"errors"
	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type TorrentioScraper struct {
	config        *config.Config
	db            *database.DB
	log           *logger.Logger
	name          string
	client        *http.Client
	lastRequest   time.Time
	currentItem   *database.WatchlistItem
	currentItemID int32
}

type Stream struct {
	Name          string        `json:"name"`
	Title         string        `json:"title"`
	InfoHash      string        `json:"infoHash"`
	FileIdx       int           `json:"fileIdx,omitempty"`
	BehaviorHints BehaviorHints `json:"behaviorHints"`
	ParsedInfo    ParsedInfo    // Will be filled after parsing
	Score         int
}

type BehaviorHints struct {
	BingeGroup string `json:"bingeGroup"`
	Filename   string `json:"filename,omitempty"`
}

type ParsedInfo struct {
	Resolution      string
	Codec           string
	FileSize        string
	Seeds           int
	Source          string
	Title           string
	Languages       []string
	DistanceFromMax float64
	SizeScore       int
	Season          int
	EpisodeCount    int
}

type TorrentioResponse struct {
	Streams []Stream `json:"streams"`
}

func NewTorrentioScraper(cfg *config.Config, db *database.DB, name string, scraperConfig config.ScraperConfig) *TorrentioScraper {
	timeout := time.Duration(scraperConfig.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
	}

	return &TorrentioScraper{
		config: cfg,
		db:     db,
		log:    logger.New(),
		name:   name,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (s *TorrentioScraper) Name() string {
	return s.name
}

func (s *TorrentioScraper) Scrape(item *database.WatchlistItem) error {
	s.currentItem = item
	s.currentItemID = int32(item.ID)

	s.log.Info("TorrentioScraper", "Scrape", fmt.Sprintf("Starting scrape for item: %s (ID: %d)", item.Title, item.ID))
	s.log.Debug("TorrentioScraper", "Scrape", fmt.Sprintf("Media type: %v (Valid: %v)", item.MediaType.String, item.MediaType.Valid))

	// Only check release dates for movies
	// TV shows are handled episode by episode in scrapeTVShow
	if item.MediaType.Valid && item.MediaType.String == "movie" {
		if item.ReleaseDate.Valid && !item.ReleaseDate.Time.IsZero() {
			if item.ReleaseDate.Time.After(time.Now()) {
				s.log.Info("TorrentioScraper", "Scrape", fmt.Sprintf("Skipping unreleased movie: %s (Release date: %s)", item.Title, item.ReleaseDate.Time.Format("2006-01-02")))
				return s.saveFailedScrapeResult(item, "movie not yet released")
			}
		} else {
			// If no release date or zero date (0001-01-01), treat as unreleased
			s.log.Info("TorrentioScraper", "Scrape", fmt.Sprintf("Skipping movie with no valid release date: %s", item.Title))
			return s.saveFailedScrapeResult(item, "movie has no valid release date")
		}
	}

	if item.MediaType.Valid && item.MediaType.String == "tv" {
		s.log.Debug("TorrentioScraper", "Scrape", "Handling TV show")
		return s.scrapeTVShow(item)
	} else if item.MediaType.Valid && item.MediaType.String == "movie" {
		s.log.Debug("TorrentioScraper", "Scrape", "Handling movie")
		return s.scrapeMovie(item)
	}

	mediaType := "unknown"
	if item.MediaType.Valid {
		mediaType = item.MediaType.String
	}
	s.log.Warning("TorrentioScraper", "Scrape", fmt.Sprintf("Unsupported media type: %v", mediaType))
	return errors.New("unsupported item type: " + mediaType)
}

// Helper function to save a failed scrape result
func (s *TorrentioScraper) saveFailedScrapeResult(item *database.WatchlistItem, reason string) error {
	result := &database.ScrapeResult{
		WatchlistItemID: sql.NullInt32{Int32: int32(item.ID), Valid: true},
		StatusResults:   sql.NullString{String: "scraping_failed", Valid: true},
		ScrapedDate:     sql.NullTime{Time: time.Now(), Valid: true},
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if _, err := s.saveScrapeResult(item, result); err != nil {
		s.log.Error("TorrentioScraper", "Scrape", fmt.Sprintf("Failed to save failed scrape result: %v", err))
	}
	return errors.New(reason)
}

func (s *TorrentioScraper) scrapeTVShow(item *database.WatchlistItem) error {
	s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Starting scrape for TV show: %s (ID: %d)", item.Title, item.ID))

	// First get the list of seasons
	seasons, err := s.db.GetSeasonsForItem(item.ID)
	if err != nil {
		s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to get seasons for show %s: %v", item.Title, err))
		return fmt.Errorf("failed to get seasons")
	}
	s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Found %d seasons for show %s", len(seasons), item.Title))

	// Get all episodes for each season
	for i := range seasons {
		episodes, err := s.db.GetEpisodesForSeason(seasons[i].ID)
		if err != nil {
			s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to get episodes for season %d: %v", seasons[i].SeasonNumber, err))
			return fmt.Errorf("failed to get episodes")
		}
		s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Season %d has %d episodes", seasons[i].SeasonNumber, len(episodes)))

		// Check if we need to scrape this season
		needsScraping := false
		for _, episode := range episodes {
			// Check if this episode already has a successful scrape result
			results, err := s.db.GetScrapeResultsByEpisode(episode.ID)
			if err != nil || len(results) == 0 {
				needsScraping = true
				break
			}
			// Check if any result is ready for download
			hasReady := false
			for _, result := range results {
				if result.StatusResults.String == "scraped" {
					hasReady = true
					break
				}
			}
			if !hasReady {
				needsScraping = true
				break
			}
		}

		if !needsScraping {
			s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Season %d already has all episodes scraped", seasons[i].SeasonNumber))
			continue
		}

		// Check if the season has ended
		seasonEnded := true
		for _, episode := range episodes {
			if episode.AirDate.Valid {
				// If any episode's air date is in the future, the season hasn't ended
				if episode.AirDate.Time.After(time.Now()) {
					seasonEnded = false
					break
				}
			} else {
				// If any episode doesn't have an air date, assume season hasn't ended
				seasonEnded = false
				break
			}
		}

		// Query for any episode in the season - the API will return season packs too
		// We'll use the first episode as our query point
		if len(episodes) > 0 {
			query := fmt.Sprintf("S%02dE%02d", seasons[i].SeasonNumber, episodes[0].EpisodeNumber)
			response, err := s.searchTorrentio(item, query)
			if err != nil || response == nil {
				s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to search for season %d: %v", seasons[i].SeasonNumber, err))
				continue
			}

			// Process streams and look for season packs first (only if season has ended)
			var bestStream *Stream
			var isSeasonPack bool

			for j := range response.Streams {
				stream := &response.Streams[j]
				info := s.parseStreamInfo(stream.Title)
				stream.ParsedInfo = info
				stream.Score = s.calculateScore(stream)

				// Check if this is a season pack for our season (only if season has ended)
				if seasonEnded &&
					strings.Contains(stream.Title, fmt.Sprintf("S%02d", seasons[i].SeasonNumber)) &&
					!strings.Contains(stream.Title, fmt.Sprintf("E%02d", episodes[0].EpisodeNumber)) {
					// Looks like a season pack
					if bestStream == nil || stream.Score > bestStream.Score {
						bestStream = stream
						isSeasonPack = true
					}
				} else if !isSeasonPack && (bestStream == nil || stream.Score > bestStream.Score) {
					// Individual episode, only use if we haven't found a season pack
					bestStream = stream
				}
			}

			if bestStream != nil {
				if isSeasonPack {
					s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Found season pack for completed season %d: %s", seasons[i].SeasonNumber, bestStream.Title))
					if err := s.processSeasonPack(*bestStream, item, seasons[i]); err != nil {
						s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to process season pack: %v", err))
						// Fall through to individual episodes
					} else {
						continue // Successfully processed season pack, move to next season
					}
				} else {
					// Create scrape result for the individual episode
					result := &database.ScrapeResult{
						WatchlistItemID:   sql.NullInt32{Int32: int32(item.ID), Valid: true},
						InfoHash:          sql.NullString{String: bestStream.InfoHash, Valid: true},
						ScrapedFilename:   sql.NullString{String: bestStream.Title, Valid: true},
						ScrapedResolution: sql.NullString{String: bestStream.ParsedInfo.Resolution, Valid: true},
						ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
						ScrapedScore:      sql.NullInt32{Int32: int32(bestStream.Score), Valid: true},
						ScrapedCodec:      sql.NullString{String: bestStream.ParsedInfo.Codec, Valid: true},
						StatusResults:     sql.NullString{String: "scraped", Valid: true},
						CreatedAt:         time.Now(),
						UpdatedAt:         time.Now(),
					}

					// Save scrape result and update episode reference
					if id, err := s.saveScrapeResult(item, result); err != nil {
						s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to save scrape result: %v", err))
					} else {
						episodes[0].ScrapeResultID = sql.NullInt32{Int32: int32(id), Valid: true}
						if err := s.db.UpdateTVEpisode(&episodes[0]); err != nil {
							s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to update episode scrape result ID: %v", err))
						}
					}
				}
			}

			// Process remaining episodes individually if we didn't find a season pack or if season is still airing
			if !isSeasonPack {
				reason := "no season pack found"
				if !seasonEnded {
					reason = "season still airing"
				}
				s.log.Info("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Processing episodes individually for season %d (%s)", seasons[i].SeasonNumber, reason))
				if err := s.scrapeIndividualEpisodes(item, seasons[i], episodes[1:]); err != nil {
					s.log.Error("TorrentioScraper", "scrapeTVShow", fmt.Sprintf("Failed to scrape remaining episodes: %v", err))
				}
			}
		}
	}

	return nil
}

func (s *TorrentioScraper) scrapeIndividualEpisodes(item *database.WatchlistItem, season *database.Season, episodes []database.TVEpisode) error {
	currentTime := time.Now()
	var lastErr error
	var foundAny bool

	for _, episode := range episodes {
		// Skip episodes that haven't been released yet
		if episode.AirDate.Valid && episode.AirDate.Time.After(currentTime) {
			s.log.Info("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Skipping future episode %s S%02dE%02d (air date: %s)",
					item.Title, season.SeasonNumber, episode.EpisodeNumber,
					episode.AirDate.Time.Format("2006-01-02")))
			continue
		}

		// Skip episodes that have already been scraped
		if episode.Scraped {
			s.log.Info("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Skipping already scraped episode %s S%02dE%02d",
					item.Title, season.SeasonNumber, episode.EpisodeNumber))
			foundAny = true
			continue
		}

		// Try to find streams for this episode
		response, err := s.searchTorrentio(item, fmt.Sprintf("S%02dE%02d", season.SeasonNumber, episode.EpisodeNumber))
		if err != nil {
			s.log.Warning("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Failed to get streams for episode %d: %v", episode.EpisodeNumber, err))
			lastErr = err
			continue
		}

		if len(response.Streams) == 0 {
			s.log.Warning("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("No streams found for episode %d", episode.EpisodeNumber))
			lastErr = fmt.Errorf("no streams found for episode %d", episode.EpisodeNumber)
			continue
		}

		// Sort streams by score
		sort.Slice(response.Streams, func(i, j int) bool {
			return response.Streams[i].Score > response.Streams[j].Score
		})

		// Take the highest scoring stream
		stream := response.Streams[0]

		// Create scrape result for the episode
		result := &database.ScrapeResult{
			WatchlistItemID:   sql.NullInt32{Int32: int32(item.ID), Valid: true},
			ScrapedFilename:   sql.NullString{String: stream.BehaviorHints.Filename, Valid: true},
			ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
			ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "scraped", Valid: true},
		}

		// Save scrape result and update episode reference
		if id, err := s.saveScrapeResult(item, result); err != nil {
			s.log.Error("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Failed to save scrape result for episode %d: %v", episode.EpisodeNumber, err))
			lastErr = fmt.Errorf("failed to save scrape result for episode %d: %v", episode.EpisodeNumber, err)
			continue
		} else {
			episode.ScrapeResultID = sql.NullInt32{Int32: int32(id), Valid: true}
			episode.Scraped = true
			if err := s.db.UpdateTVEpisode(&episode); err != nil {
				s.log.Error("TorrentioScraper", "scrapeIndividualEpisodes",
					fmt.Sprintf("Failed to update episode %d: %v", episode.EpisodeNumber, err))
				lastErr = fmt.Errorf("failed to update episode %d: %v", episode.EpisodeNumber, err)
				continue
			}
		}

		s.log.Info("TorrentioScraper", "Database",
			fmt.Sprintf("Saved scrape result for %s S%02dE%02d: %s (Score: %d)",
				item.Title, season.SeasonNumber, episode.EpisodeNumber,
				result.ScrapedFilename.String, result.ScrapedScore.Int32))

		foundAny = true
	}

	if !foundAny && lastErr != nil {
		return fmt.Errorf("failed to scrape any episodes: %v", lastErr)
	}

	return nil
}

func (s *TorrentioScraper) scrapeMovie(item *database.WatchlistItem) error {
	// Try to find streams for the movie
	response, err := s.searchTorrentio(item, "")
	if err != nil {
		s.log.Warning("TorrentioScraper", "scrapeMovie",
			fmt.Sprintf("Failed to get streams for movie: %v", err))
		return err
	}

	if len(response.Streams) == 0 {
		s.log.Warning("TorrentioScraper", "scrapeMovie", "No streams found")
		return fmt.Errorf("no streams found")
	}

	// Sort streams by score
	sort.Slice(response.Streams, func(i, j int) bool {
		return response.Streams[i].Score > response.Streams[j].Score
	})

	// Take the highest scoring stream
	stream := response.Streams[0]

	// Create scrape result for the movie
	result := &database.ScrapeResult{
		WatchlistItemID:   sql.NullInt32{Int32: int32(item.ID), Valid: true},
		ScrapedFilename:   sql.NullString{String: stream.BehaviorHints.Filename, Valid: true},
		ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
		ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
		InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
		ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
		ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
		StatusResults:     sql.NullString{String: "scraped", Valid: true},
	}

	// Save scrape result
	if _, err := s.saveScrapeResult(item, result); err != nil {
		s.log.Error("TorrentioScraper", "scrapeMovie",
			fmt.Sprintf("Failed to save scrape result: %v", err))
		return fmt.Errorf("scrapeMovie: failed to save scrape result: %v", err)
	}

	s.log.Info("TorrentioScraper", "Database",
		fmt.Sprintf("Saved scrape result for %s: %s (Score: %d)",
			item.Title, result.ScrapedFilename.String, result.ScrapedScore.Int32))

	return nil
}

func (s *TorrentioScraper) filterSeasonPackStreams(streams []Stream, seasonNumber int, expectedEpisodeCount int) []Stream {
	var seasonPacks []Stream
	for _, stream := range streams {
		info := s.parseStreamInfo(stream.Title)
		// Check if this is a season pack:
		// 1. Should be for the correct season
		// 2. Should contain multiple episodes (usually all episodes of the season)
		// 3. Episode count should match what we expect for this season
		if info.Season == seasonNumber && info.EpisodeCount > 1 && info.EpisodeCount >= expectedEpisodeCount {
			seasonPacks = append(seasonPacks, stream)
		}
	}
	return seasonPacks
}

func (s *TorrentioScraper) processSeasonPack(stream Stream, item *database.WatchlistItem, season *database.Season) error {
	// Get all episodes for this season
	episodes, err := s.db.GetEpisodesForSeason(season.ID)
	if err != nil {
		return fmt.Errorf("failed to get episodes: %v", err)
	}

	// Create a scrape result for each episode using the season pack info
	for i := range episodes {
		result := &database.ScrapeResult{
			WatchlistItemID:   sql.NullInt32{Int32: int32(item.ID), Valid: true},
			InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
			ScrapedFilename:   sql.NullString{String: stream.Title, Valid: true},
			ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
			ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "scraped", Valid: true},
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}

		// Save scrape result and update episode reference
		if id, err := s.saveScrapeResult(item, result); err != nil {
			return fmt.Errorf("scrapeTV_SeasonPack: failed to save scrape result: %v", err)
		} else {
			episodes[i].ScrapeResultID = sql.NullInt32{Int32: int32(id), Valid: true}
			if err := s.db.UpdateTVEpisode(&episodes[i]); err != nil {
				s.log.Error("TorrentioScraper", "processSeasonPack", fmt.Sprintf("Failed to update episode scrape result ID: %v", err))
			}
		}
	}

	return nil
}

func (s *TorrentioScraper) processStreams(streams []Stream, item *database.WatchlistItem) error {
	if len(streams) == 0 {
		return fmt.Errorf("no streams found")
	}

	s.log.Info("TorrentioScraper", "Process", fmt.Sprintf("Processing %s: Found %d streams", item.Title, len(streams)))

	// Parse all streams first
	for i := range streams {
		streams[i].ParsedInfo = s.parseStreamInfo(streams[i].Title)
		streams[i].Score = s.calculateScore(&streams[i])
	}

	// Sort streams by file size (largest first)
	sort.Slice(streams, func(i, j int) bool {
		sizeI := s.convertToGB(streams[i].ParsedInfo.FileSize)
		sizeJ := s.convertToGB(streams[j].ParsedInfo.FileSize)
		return sizeI > sizeJ
	})

	// Reset all size scores
	for i := range streams {
		streams[i].ParsedInfo.SizeScore = 0
	}

	// Get max file size based on media type
	var maxSize float64
	if strings.Contains(strings.ToLower(streams[0].Title), "show") {
		maxSize = s.config.Scraping.Filesize.Show.Max
	} else {
		maxSize = s.config.Scraping.Filesize.Movie.Max
	}

	// Find the 3 files closest to max size
	var closestStreams []int
	for i := range streams {
		sizeGB := s.convertToGB(streams[i].ParsedInfo.FileSize)
		if sizeGB <= maxSize {
			closestStreams = append(closestStreams, i)
			if len(closestStreams) == 3 {
				break
			}
		}
	}

	// Assign size scores only to the closest files
	if len(closestStreams) > 0 {
		streams[closestStreams[0]].ParsedInfo.SizeScore = s.config.Scraping.Ranking.Scoring.MaxSizeScore // 1000 points for closest
	}
	if len(closestStreams) > 1 {
		streams[closestStreams[1]].ParsedInfo.SizeScore = int(float64(s.config.Scraping.Ranking.Scoring.MaxSizeScore) * 0.8) // 800 points for second closest
	}
	if len(closestStreams) > 2 {
		streams[closestStreams[2]].ParsedInfo.SizeScore = int(float64(s.config.Scraping.Ranking.Scoring.MaxSizeScore) * 0.6) // 600 points for third closest
	}

	// Recalculate total scores
	for i := range streams {
		streams[i].Score = s.calculateBaseScore(&streams[i]) + streams[i].ParsedInfo.SizeScore
	}

	// Try with all filters first
	filteredStreams := s.filterStreams(streams, item, true, false)
	if len(filteredStreams) == 0 {
		s.log.Info("TorrentioScraper", "Process", fmt.Sprintf("%s: No streams found with size filter, retrying without filter...", item.Title))
		// Fall back to all streams
		filteredStreams = s.filterStreams(streams, item, false, false)
	}

	// Sort filtered streams by score
	sort.Slice(filteredStreams, func(i, j int) bool {
		return filteredStreams[i].Score > filteredStreams[j].Score
	})

	// Log results
	if len(filteredStreams) > 0 {
		s.log.Info("TorrentioScraper", "Process", fmt.Sprintf("%s: Found %d valid streams after filtering", item.Title, len(filteredStreams)))
		bestMatch := filteredStreams[0]
		s.log.Info("TorrentioScraper", "Process", fmt.Sprintf("%s: Best match - %s (%s, Score: %d)",
			item.Title,
			bestMatch.ParsedInfo.Title,
			bestMatch.ParsedInfo.FileSize,
			bestMatch.Score))

		// Save best match to database
		scrapeResult := &database.ScrapeResult{
			WatchlistItemID:   sql.NullInt32{Int32: int32(item.ID), Valid: true},
			ScrapedFilename:   sql.NullString{String: bestMatch.ParsedInfo.Title, Valid: true},
			ScrapedResolution: sql.NullString{String: bestMatch.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			InfoHash:          sql.NullString{String: bestMatch.InfoHash, Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(bestMatch.Score), Valid: true},
			ScrapedFileSize:   sql.NullString{String: bestMatch.ParsedInfo.FileSize, Valid: true},
			ScrapedCodec:      sql.NullString{String: bestMatch.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "scraped", Valid: true},
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}

		if _, err := s.saveScrapeResult(item, scrapeResult); err != nil {
			return fmt.Errorf("processStreams: failed to save scrape result: %v", err)
		}

		s.log.Info("TorrentioScraper", "Process", fmt.Sprintf("Saved scrape result for %s: %s (Score: %d)",
			item.Title, scrapeResult.ScrapedFilename.String, scrapeResult.ScrapedScore.Int32))

		return nil
	}

	return fmt.Errorf("no valid streams found")
}

// filterStreams applies the specified filters to the streams
func (s *TorrentioScraper) filterStreams(streams []Stream, item *database.WatchlistItem, useSize, useUploader bool) []Stream {
	var filtered []Stream

	// Get file size limits
	var minSize, maxSize float64
	if useSize {
		if item.MediaType.Valid && item.MediaType.String == "tv" {
			minSize = s.config.Scraping.Filesize.Show.Min
			maxSize = s.config.Scraping.Filesize.Show.Max
		} else {
			minSize = s.config.Scraping.Filesize.Movie.Min
			maxSize = s.config.Scraping.Filesize.Movie.Max
		}
	}

	for _, stream := range streams {
		// Apply size filter if enabled
		if useSize {
			sizeGB := s.convertToGB(stream.ParsedInfo.FileSize)
			if sizeGB < minSize || sizeGB > maxSize {
				continue
			}
		}

		// Apply uploader filter if enabled
		if useUploader {
			if !s.hasPreferredUploader(stream.Title) {
				continue
			}
		}

		filtered = append(filtered, stream)
	}

	return filtered
}

// logResults logs the filtered results
func (s *TorrentioScraper) logResults(streams []Stream) {
	if len(streams) == 0 {
		s.log.Info("TorrentioScraper", "Stream", "No streams found")
		return
	}

	maxStreams := len(streams)
	if maxStreams > 20 {
		maxStreams = 20
	}

	s.log.Info("TorrentioScraper", "Stream", fmt.Sprintf("Found %d streams after filtering", len(streams)))
	s.log.Info("TorrentioScraper", "Stream", "Top results:")

	for i := 0; i < maxStreams; i++ {
		stream := streams[i]
		// Only show size score if it's non-zero (one of the top 3 closest to max size)
		sizeScoreStr := "0"
		if stream.ParsedInfo.SizeScore > 0 {
			sizeScoreStr = fmt.Sprintf("%d", stream.ParsedInfo.SizeScore)
		}

		s.log.Info("TorrentioScraper", "Stream", fmt.Sprintf(
			"[Score:%d (Res:%d|Codec:%d|Size:%s|Seeds:%d|Uploader:%d|Lang:%d)] Seeds:%d | Size:%s | Source:%s | %s | %s | Langs:%v | %s",
			stream.Score,
			s.getResolutionScore(stream.ParsedInfo.Resolution),
			s.getCodecScore(stream.ParsedInfo.Codec),
			sizeScoreStr,
			stream.ParsedInfo.Seeds,
			s.getUploaderScore(stream.Title),
			s.getLanguageScore(stream.ParsedInfo.Languages),
			stream.ParsedInfo.Seeds,
			stream.ParsedInfo.FileSize,
			stream.ParsedInfo.Source,
			stream.ParsedInfo.Resolution,
			stream.ParsedInfo.Codec,
			stream.ParsedInfo.Languages,
			stream.ParsedInfo.Title,
		))
	}

	// Log the best match
	bestStream := streams[0]
	s.log.Info("TorrentioScraper", "Selected", fmt.Sprintf(
		"Best match -> [Score:%d (Res:%d|Codec:%d|Seeds:%d|Uploader:%d)] %s | %s | %s | Seeds:%d | Size:%s",
		bestStream.Score,
		s.getResolutionScore(bestStream.ParsedInfo.Resolution),
		s.getCodecScore(bestStream.ParsedInfo.Codec),
		bestStream.ParsedInfo.Seeds,
		s.getUploaderScore(bestStream.Title),
		bestStream.ParsedInfo.Resolution,
		bestStream.ParsedInfo.Codec,
		bestStream.ParsedInfo.Title,
		bestStream.ParsedInfo.Seeds,
		bestStream.ParsedInfo.FileSize,
	))
}

// Helper functions to get individual scores
func (s *TorrentioScraper) getResolutionScore(resolution string) int {
	switch resolution {
	case "2160p", "4k":
		return s.config.Scraping.Ranking.Scoring.ResolutionScores["2160p"]
	case "1080p":
		return s.config.Scraping.Ranking.Scoring.ResolutionScores["1080p"]
	case "720p":
		return s.config.Scraping.Ranking.Scoring.ResolutionScores["720p"]
	case "480p":
		return s.config.Scraping.Ranking.Scoring.ResolutionScores["480p"]
	}
	return 0
}

func (s *TorrentioScraper) getCodecScore(codec string) int {
	switch codec {
	case "x265", "HEVC", "h265":
		return s.config.Scraping.Ranking.Scoring.CodecScores["hevc"]
	case "x264", "AVC", "h264":
		return s.config.Scraping.Ranking.Scoring.CodecScores["avc"]
	}
	return 0
}

func (s *TorrentioScraper) getUploaderScore(title string) int {
	if s.hasPreferredUploader(title) {
		return s.config.Scraping.Ranking.Scoring.PreferredUploaderScore
	}
	return 0
}

func (s *TorrentioScraper) getLanguageScore(languages []string) int {
	score := 0
	for _, lang := range languages {
		for _, includedLang := range s.config.Scraping.Languages.Include {
			if lang == includedLang {
				score += s.config.Scraping.Ranking.Scoring.LanguageIncludeScore
			}
		}
		for _, excludedLang := range s.config.Scraping.Languages.Exclude {
			if lang == excludedLang {
				score += s.config.Scraping.Ranking.Scoring.LanguageExcludePenalty
			}
		}
	}
	return score
}

func (s *TorrentioScraper) parseStreamInfo(title string) ParsedInfo {
	info := ParsedInfo{}

	// Split the title into parts by newline
	parts := strings.Split(title, "\n")
	if len(parts) > 0 {
		info.Title = strings.TrimSpace(parts[0])
	}

	// Parse metadata if available (second line)
	if len(parts) > 1 {
		metadata := parts[1]

		// Parse Seeds (👤)
		if idx := strings.Index(metadata, "👤"); idx != -1 {
			seedStr := strings.TrimSpace(strings.Split(metadata[idx+3:], " ")[0])
			seedStr = strings.TrimFunc(seedStr, func(r rune) bool {
				return !unicode.IsDigit(r)
			})
			info.Seeds, _ = strconv.Atoi(seedStr)
		}

		// Parse File Size (💾)
		if idx := strings.Index(metadata, "💾"); idx != -1 {
			sizeStr := metadata[idx+3:]
			if endIdx := strings.Index(sizeStr, "⚙️"); endIdx != -1 {
				// Extract just the numeric part and unit
				rawSize := strings.TrimSpace(sizeStr[:endIdx])
				var value float64
				var unit string

				// Try to parse with regex to extract just the number and unit
				for _, part := range strings.Fields(rawSize) {
					// Skip any part that starts with a special character
					if strings.IndexFunc(part, func(r rune) bool {
						return r > 127
					}) == 0 {
						continue
					}

					// Try to parse as number
					if v, err := strconv.ParseFloat(part, 64); err == nil {
						value = v
						continue
					}

					// Must be the unit
					if strings.Contains(strings.ToUpper(part), "GB") {
						unit = "GB"
					}
				}

				if value > 0 && unit != "" {
					info.FileSize = fmt.Sprintf("%.2f %s", value, unit)
				}
			}
		}

		// Parse Source (⚙️)
		if idx := strings.Index(metadata, "⚙️"); idx != -1 {
			rest := strings.TrimSpace(metadata[idx+3:])
			info.Source = strings.TrimSpace(rest)
		}
	}

	// Parse language flags if available (third line)
	if len(parts) > 2 {
		langLine := parts[2]
		info.Languages = s.parseLanguages(langLine)
	}

	// Parse resolution and codec from the title
	titleLower := strings.ToLower(info.Title)

	// Resolution detection
	for _, res := range []string{"2160p", "1080p", "720p", "480p", "4k"} {
		if strings.Contains(titleLower, strings.ToLower(res)) {
			info.Resolution = res
			break
		}
	}

	// Codec detection
	for _, codec := range []string{"x265", "hevc", "h265", "x264", "avc", "h264"} {
		if strings.Contains(titleLower, strings.ToLower(codec)) {
			info.Codec = codec
			break
		}
	}

	// Parse season and episode count
	if strings.Contains(titleLower, "season") || strings.Contains(titleLower, "complete") {
		season := 0
		episodeCount := 0
		if strings.Contains(titleLower, "season") {
			seasonStr := strings.Split(titleLower, "season")[1]
			seasonStr = strings.TrimSpace(strings.Split(seasonStr, " ")[0])
			season, _ = strconv.Atoi(seasonStr)
		}
		if strings.Contains(titleLower, "complete") {
			episodeCountStr := strings.Split(titleLower, "complete")[1]
			episodeCountStr = strings.TrimSpace(strings.Split(episodeCountStr, " ")[0])
			episodeCount, _ = strconv.Atoi(episodeCountStr)
		}
		info.Season = season
		info.EpisodeCount = episodeCount
	}

	return info
}

// Helper function to parse language emoji flags
func (s *TorrentioScraper) parseLanguages(str string) []string {
	var languages []string

	// Split the string into runes
	runes := []rune(str)
	for i := 0; i < len(runes)-1; i++ {
		// Check for regional indicator symbols
		if isRegionalIndicator(runes[i]) && isRegionalIndicator(runes[i+1]) {
			firstLetter := string(rune(runes[i] - 0x1F1E6 + 'A'))
			secondLetter := string(rune(runes[i+1] - 0x1F1E6 + 'A'))
			countryCode := firstLetter + secondLetter
			languages = append(languages, countryCode)
			i++ // Skip the second rune
		}
	}

	return languages
}

// Helper function to check if a rune is a regional indicator symbol
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

func (s *TorrentioScraper) calculateScore(stream *Stream) int {
	return s.calculateBaseScore(stream) + stream.ParsedInfo.SizeScore
}

func (s *TorrentioScraper) calculateBaseScore(stream *Stream) int {
	score := 0
	config := s.config.Scraping.Ranking.Scoring

	// Score based on resolution
	switch stream.ParsedInfo.Resolution {
	case "2160p", "4k":
		score += config.ResolutionScores["2160p"]
	case "1080p":
		score += config.ResolutionScores["1080p"]
	case "720p":
		score += config.ResolutionScores["720p"]
	case "480p":
		score += config.ResolutionScores["480p"]
	}

	// Score based on codec
	switch stream.ParsedInfo.Codec {
	case "x265", "HEVC", "h265":
		score += config.CodecScores["hevc"]
	case "x264", "AVC", "h264":
		score += config.CodecScores["avc"]
	}

	// Score based on seeders (capped at maxSeederScore)
	seedScore := stream.ParsedInfo.Seeds
	if seedScore > config.MaxSeederScore {
		seedScore = config.MaxSeederScore
	}
	score += seedScore

	// Add preferred uploader score if applicable
	if s.hasPreferredUploader(stream.Title) {
		score += config.PreferredUploaderScore
	}

	// Score based on languages
	for _, lang := range stream.ParsedInfo.Languages {
		for _, includedLang := range s.config.Scraping.Languages.Include {
			if lang == includedLang {
				score += config.LanguageIncludeScore
			}
		}
		for _, excludedLang := range s.config.Scraping.Languages.Exclude {
			if lang == excludedLang {
				score += config.LanguageExcludePenalty
			}
		}
	}

	return score
}

// Helper function to convert size string to GB
func (s *TorrentioScraper) convertToGB(sizeStr string) float64 {
	// Remove any non-ASCII characters and trim spaces
	cleaned := strings.Map(func(r rune) rune {
		if r > 127 {
			return -1 // Drop non-ASCII characters
		}
		return r
	}, sizeStr)

	cleaned = strings.TrimSpace(cleaned)

	// Handle empty input
	if cleaned == "" {
		return 0
	}

	var value float64
	var unit string

	// Try to parse with different formats
	n, err := fmt.Sscanf(cleaned, "%f %s", &value, &unit)
	if err != nil || n != 2 {
		n, err = fmt.Sscanf(cleaned, "%f%s", &value, &unit)
		if err != nil || n != 2 {
			return 0
		}
	}

	// Convert unit to uppercase for comparison
	unit = strings.ToUpper(unit)

	switch unit {
	case "TB", "TIB":
		return value * 1024
	case "GB", "GIB":
		return value
	case "MB", "MIB":
		return value / 1024
	case "KB", "KIB":
		return value / (1024 * 1024)
	default:
		return 0
	}
}

// Helper function to check if a title contains a preferred uploader
func (s *TorrentioScraper) hasPreferredUploader(title string) bool {
	title = strings.ToUpper(title)
	for _, uploaderGroup := range s.config.Scraping.PreferredUploaders {
		// Split the comma-separated values
		uploaders := strings.Split(uploaderGroup, ",")
		for _, uploader := range uploaders {
			uploader = strings.TrimSpace(strings.ToUpper(uploader))
			// Check for common separators: -, ., [, ]
			searchTerms := []string{
				uploader,
				"-" + uploader,
				"." + uploader,
				"[" + uploader + "]",
			}

			for _, term := range searchTerms {
				if strings.Contains(title, term) {
					return true
				}
			}
		}
	}
	return false
}

func (s *TorrentioScraper) saveScrapeResult(item *database.WatchlistItem, result *database.ScrapeResult) (int, error) {
	// Don't override the status here, let the caller set it
	id, err := s.db.SaveScrapeResult(result)
	if err != nil {
		s.log.Error("TorrentioScraper", "saveScrapeResult", fmt.Sprintf("Failed to save scrape result: %v", err))
		return 0, err
	}
	s.log.Info("TorrentioScraper", "Database", fmt.Sprintf("Saved scrape result for %s: %s (Score: %d)", item.Title, result.ScrapedFilename.String, result.ScrapedScore.Int32))
	return id, nil
}

func (s *TorrentioScraper) makeRequest(url string) ([]byte, error) {
	maxRetries := 3
	initialBackoff := 1 * time.Second
	maxBackoff := 5 * time.Second

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		httpResp, err := s.client.Get(url)
		if err != nil {
			lastErr = err
			backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(i)))
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			s.log.Warning("TorrentioScraper", "makeRequest", fmt.Sprintf("Request error, waiting %v before retry %d/%d", backoff, i+1, maxRetries))
			time.Sleep(backoff)
			continue
		}
		defer httpResp.Body.Close()

		if httpResp.StatusCode == 500 {
			lastErr = fmt.Errorf("server error 500")
			backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(i)))
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			s.log.Warning("TorrentioScraper", "makeRequest", fmt.Sprintf("Server error 500, waiting %v before retry %d/%d", backoff, i+1, maxRetries))
			time.Sleep(backoff)
			continue
		}

		if httpResp.StatusCode != 200 {
			return nil, fmt.Errorf("unexpected status code: %d", httpResp.StatusCode)
		}

		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}

		return body, nil
	}

	// Create a failed scrape result only if we have a current item
	if s.currentItem != nil {
		result := &database.ScrapeResult{
			WatchlistItemID: sql.NullInt32{Int32: int32(s.currentItem.ID), Valid: true},
			StatusResults:   sql.NullString{String: "scraping_failed", Valid: true},
			ScrapedDate:     sql.NullTime{Time: time.Now(), Valid: true},
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if _, err := s.saveScrapeResult(s.currentItem, result); err != nil {
			s.log.Error("TorrentioScraper", "makeRequest", fmt.Sprintf("Failed to save failed scrape result: %v", err))
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %v", maxRetries, lastErr)
}

func (s *TorrentioScraper) searchTorrentio(item *database.WatchlistItem, query string) (*TorrentioResponse, error) {
	var urls []string

	// Get the filter from config
	filter := s.config.Scraping.Scrapers["torrentio"].Filter

	if item.ImdbID.Valid && item.ImdbID.String != "" {
		// For TV shows, we need to append the season and episode numbers
		if item.MediaType.Valid && item.MediaType.String == "tv" {
			// Parse season and episode from query (format: "S01E02")
			seasonStr := query[1:3]  // Extract "01" from "S01E02"
			episodeStr := query[4:6] // Extract "02" from "S01E02"

			url := fmt.Sprintf("%s/stream/series/%s:%s:%s.json",
				s.config.Scraping.Scrapers["torrentio"].URL,
				item.ImdbID.String,
				seasonStr,
				episodeStr)

			// Add filter if present
			if filter != "" {
				// Split the URL at the first forward slash after the domain
				parts := strings.SplitN(url, "/stream/", 2)
				if len(parts) == 2 {
					url = fmt.Sprintf("%s/%s/stream/%s", parts[0], filter, parts[1])
				}
			}

			urls = append(urls, url)
		} else {
			// For movies
			url := fmt.Sprintf("%s/stream/movie/%s.json",
				s.config.Scraping.Scrapers["torrentio"].URL,
				item.ImdbID.String)

			// Add filter if present
			if filter != "" {
				// Split the URL at the first forward slash after the domain
				parts := strings.SplitN(url, "/stream/", 2)
				if len(parts) == 2 {
					url = fmt.Sprintf("%s/%s/stream/%s", parts[0], filter, parts[1])
				}
			}

			urls = append(urls, url)
		}
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("no valid ID found for item")
	}

	var lastErr error
	var allStreams []Stream

	for _, url := range urls {
		s.log.Info("TorrentioScraper", "searchTorrentio",
			fmt.Sprintf("Trying URL: %s", url))

		body, err := s.makeRequest(url)
		if err != nil {
			lastErr = err
			continue
		}

		var response TorrentioResponse
		if err := json.Unmarshal(body, &response); err != nil {
			lastErr = err
			continue
		}

		// Process each stream
		for i := range response.Streams {
			// Parse stream info
			response.Streams[i].ParsedInfo = s.parseStreamInfo(response.Streams[i].Title)
			// Calculate score
			response.Streams[i].Score = s.calculateScore(&response.Streams[i])
		}

		// Append streams from this response
		allStreams = append(allStreams, response.Streams...)
	}

	// Sort all streams by score
	sort.Slice(allStreams, func(i, j int) bool {
		return allStreams[i].Score > allStreams[j].Score
	})

	if len(allStreams) > 0 {
		return &TorrentioResponse{Streams: allStreams}, nil
	}

	// Create a failed scrape result
	result := &database.ScrapeResult{
		WatchlistItemID: sql.NullInt32{Int32: int32(item.ID), Valid: true},
		StatusResults:   sql.NullString{String: "scraping_failed", Valid: true},
		ScrapedDate:     sql.NullTime{Time: time.Now(), Valid: true},
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if _, err := s.saveScrapeResult(item, result); err != nil {
		s.log.Error("TorrentioScraper", "searchTorrentio", fmt.Sprintf("Failed to save failed scrape result: %v", err))
	}

	// If we get here, all URLs failed
	return nil, fmt.Errorf("all scraping attempts failed: %v", lastErr)
}
