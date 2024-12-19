package scraper

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type TorrentioScraper struct {
	config *config.Config
	db     *database.DB
	log    *logger.Logger
	name   string
	client *http.Client
	lastRequest time.Time
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
	if item.MediaType.Valid && item.MediaType.String == "tv" {
		return s.scrapeTVShow(item)
	}

	// Existing movie scraping logic
	var urls []string

	if item.ImdbID.Valid && item.ImdbID.String != "" {
		// Remove 'tt' prefix if present
		imdbID := strings.TrimPrefix(item.ImdbID.String, "tt")
		urls = append(urls, fmt.Sprintf("%s/stream/movie/tt%s.json", s.config.Scraping.Scrapers["torrentio"].URL, imdbID))
	}

	if item.TmdbID.Valid && item.TmdbID.String != "" {
		urls = append(urls, fmt.Sprintf("%s/stream/movie/tmdb:%s.json", s.config.Scraping.Scrapers["torrentio"].URL, item.TmdbID.String))
	}

	if len(urls) == 0 {
		return fmt.Errorf("no valid ID found for item")
	}

	var lastErr error
	// Try each URL until one works
	for _, url := range urls {
		s.log.Info("TorrentioScraper", "Scrape", fmt.Sprintf("Trying URL for %s: %s", item.Title, url))

		resp, err := s.makeRequest(url)
		if err != nil {
			lastErr = err
			s.log.Warning("TorrentioScraper", "Scrape", fmt.Sprintf("Failed to fetch from %s: %v", url, err))
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status code: %d for URL %s", resp.StatusCode, url)
			s.log.Warning("TorrentioScraper", "Scrape", lastErr.Error())
			continue
		}

		// Process response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %v", err)
			continue
		}

		var response TorrentioResponse

		if err := json.Unmarshal(body, &response); err != nil {
			lastErr = fmt.Errorf("failed to parse response: %v", err)
			continue
		}

		existingHash, err := s.db.GetExistingHashForItem(item.ID)
		if err != nil {
			return fmt.Errorf("failed to get existing hash: %v", err)
		}

		// Filter out streams with the existing hash
		filteredStreams := []Stream{}
		for _, stream := range response.Streams {
			if stream.InfoHash != existingHash {
				filteredStreams = append(filteredStreams, stream)
			} else {
				// Update the status of the matching scrape result to "ignored hash"
				if err := s.db.UpdateScrapeResultStatus(item.ID, "ignored hash"); err != nil {
					s.log.Error("TorrentioScraper", "Scrape", fmt.Sprintf("Failed to update status for ignored hash: %v", err))
				}
			}
		}

		if len(filteredStreams) == 0 {
			return fmt.Errorf("no valid streams found after filtering")
		}

		// Proceed with filtered streams
		return s.processStreams(filteredStreams, item)
	}

	return fmt.Errorf("all URLs failed. Last error: %v", lastErr)
}

func (s *TorrentioScraper) scrapeTVShow(item *database.WatchlistItem) error {
	// First get the list of seasons
	seasons, err := s.db.GetSeasonsForItem(item.ID)
	if err != nil {
		return fmt.Errorf("failed to get TV seasons: %w", err)
	}

	// Get all episodes for each season
	type episodeWithSeason struct {
		episode      database.TVEpisode
		seasonNumber int
	}
	var allEpisodes []episodeWithSeason

	for _, season := range seasons {
		episodes, err := s.db.GetEpisodesForSeason(season.ID)
		if err != nil {
			return fmt.Errorf("failed to get TV episodes for season %d: %w", season.SeasonNumber, err)
		}
		for _, episode := range episodes {
			allEpisodes = append(allEpisodes, episodeWithSeason{
				episode:      episode,
				seasonNumber: season.SeasonNumber,
			})
		}
	}

	// Get all streams for the show at once
	showURL := fmt.Sprintf("%s/stream/show/%s.json",
		s.config.Scraping.Scrapers["torrentio"].URL, item.ImdbID.String)

	resp, err := s.makeRequest(showURL)
	if err != nil {
		return fmt.Errorf("failed to get show streams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var response TorrentioResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	currentTime := time.Now()
	foundAny := false

	// Process each episode
	for _, episodeInfo := range allEpisodes {
		episode := episodeInfo.episode

		// Skip episodes that haven't been released yet
		if episode.AirDate.Valid && episode.AirDate.Time.After(currentTime) {
			s.log.Info("TorrentioScraper", "scrapeTVShow",
				fmt.Sprintf("Skipping future episode %s S%02dE%02d (air date: %s)",
					item.Title, episodeInfo.seasonNumber, episode.EpisodeNumber,
					episode.AirDate.Time.Format("2006-01-02")))
			continue
		}

		// Skip episodes that have already been scraped
		if episode.Scraped {
			s.log.Info("TorrentioScraper", "scrapeTVShow",
				fmt.Sprintf("Skipping already scraped episode %s S%02dE%02d",
					item.Title, episodeInfo.seasonNumber, episode.EpisodeNumber))
			foundAny = true
			continue
		}

		// Filter streams for this episode
		var episodeStreams []Stream
		episodePattern := fmt.Sprintf("S%02dE%02d", episodeInfo.seasonNumber, episode.EpisodeNumber)
		for _, stream := range response.Streams {
			if strings.Contains(stream.Title, episodePattern) {
				// Parse stream info (resolution, codec, etc.)
				stream.ParsedInfo = s.parseStreamInfo(stream.Title)
				stream.Score = s.calculateScore(&stream)
				episodeStreams = append(episodeStreams, stream)
			}
		}

		if len(episodeStreams) == 0 {
			s.log.Warning("TorrentioScraper", "scrapeTVShow",
				fmt.Sprintf("No streams found for episode %d", episode.EpisodeNumber))
			continue
		}

		// Sort streams by score
		sort.Slice(episodeStreams, func(i, j int) bool {
			return episodeStreams[i].Score > episodeStreams[j].Score
		})

		// Take the highest scoring stream
		stream := episodeStreams[0]

		// Create scrape result for the episode
		result := &database.ScrapeResult{
			WatchlistItemID:   item.ID,
			ScrapedFilename:   sql.NullString{String: stream.BehaviorHints.Filename, Valid: true},
			ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
			ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "scraped", Valid: true},
		}

		// Save scrape result
		scrapeResultID, err := s.db.SaveScrapeResult(result)
		if err != nil {
			s.log.Error("TorrentioScraper", "scrapeTVShow",
				fmt.Sprintf("Failed to save scrape result for episode %d: %v", episode.EpisodeNumber, err))
			continue
		}

		// Update episode with scrape result
		episode.ScrapeResultID = sql.NullInt32{Int32: int32(scrapeResultID), Valid: true}
		episode.Scraped = true
		if err := s.db.UpdateTVEpisode(&episode); err != nil {
			s.log.Error("TorrentioScraper", "scrapeTVShow",
				fmt.Sprintf("Failed to update episode %d: %v", episode.EpisodeNumber, err))
			continue
		}

		s.log.Info("TorrentioScraper", "Database",
			fmt.Sprintf("Saved scrape result for %s S%02dE%02d: %s (Score: %d)",
				item.Title, episodeInfo.seasonNumber, episode.EpisodeNumber,
				result.ScrapedFilename.String, result.ScrapedScore.Int32))

		foundAny = true
	}

	if !foundAny {
		return fmt.Errorf("failed to scrape any episodes")
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
			WatchlistItemID:   item.ID,
			ScrapedFilename:   sql.NullString{String: stream.BehaviorHints.Filename, Valid: true},
			ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
			ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "pending_download", Valid: true},
		}

		// Save scrape result
		scrapeResultID, err := s.db.SaveScrapeResult(result)
		if err != nil {
			s.log.Error("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Failed to save scrape result for episode %d: %v", episode.EpisodeNumber, err))
			lastErr = fmt.Errorf("failed to save scrape result for episode %d: %v", episode.EpisodeNumber, err)
			continue
		}

		// Update episode with scrape result
		episode.ScrapeResultID = sql.NullInt32{Int32: int32(scrapeResultID), Valid: true}
		episode.Scraped = true
		if err := s.db.UpdateTVEpisode(&episode); err != nil {
			s.log.Error("TorrentioScraper", "scrapeIndividualEpisodes",
				fmt.Sprintf("Failed to update episode %d: %v", episode.EpisodeNumber, err))
			lastErr = fmt.Errorf("failed to update episode %d: %v", episode.EpisodeNumber, err)
			continue
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

func (s *TorrentioScraper) filterSeasonPackStreams(streams []Stream, seasonNumber int, expectedEpisodeCount int) []Stream {
	var seasonPacks []Stream
	for _, stream := range streams {
		// Parse the stream info to get more details
		if stream.ParsedInfo.Season == seasonNumber && stream.ParsedInfo.EpisodeCount == expectedEpisodeCount {
			seasonPacks = append(seasonPacks, stream)
		}
	}

	// Sort season packs by score
	sort.Slice(seasonPacks, func(i, j int) bool {
		return seasonPacks[i].Score > seasonPacks[j].Score
	})

	return seasonPacks
}

func (s *TorrentioScraper) processSeasonPack(stream Stream, item *database.WatchlistItem, season *database.Season) error {
	// Create scrape result for the season pack
	result := &database.ScrapeResult{
		WatchlistItemID:   item.ID,
		ScrapedFilename:   sql.NullString{String: stream.BehaviorHints.Filename, Valid: true},
		ScrapedResolution: sql.NullString{String: stream.ParsedInfo.Resolution, Valid: true},
		ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
		InfoHash:          sql.NullString{String: stream.InfoHash, Valid: true},
		ScrapedScore:      sql.NullInt32{Int32: int32(stream.Score), Valid: true},
		ScrapedCodec:      sql.NullString{String: stream.ParsedInfo.Codec, Valid: true},
		StatusResults:     sql.NullString{String: "ready_for_download", Valid: true},
	}

	// Save scrape result
	scrapeResultID, err := s.db.SaveScrapeResult(result)
	if err != nil {
		s.log.Error("TorrentioScraper", "processSeasonPack",
			fmt.Sprintf("Failed to save scrape result: %v", err))
		return fmt.Errorf("failed to save scrape result: %v", err)
	}

	// Update all episodes in the season
	episodes, err := s.db.GetEpisodesForSeason(season.ID)
	if err != nil {
		s.log.Error("TorrentioScraper", "processSeasonPack",
			fmt.Sprintf("Failed to get episodes: %v", err))
		return fmt.Errorf("failed to get episodes: %v", err)
	}

	for _, ep := range episodes {
		ep.ScrapeResultID = sql.NullInt32{Int32: int32(scrapeResultID), Valid: true}
		ep.Scraped = true
		if err := s.db.UpdateTVEpisode(&ep); err != nil {
			s.log.Error("TorrentioScraper", "processSeasonPack",
				fmt.Sprintf("Failed to update episode: %v", err))
			return fmt.Errorf("failed to update episode: %v", err)
		}
	}

	return nil
}

// Add this new helper function to process the streams
func (s *TorrentioScraper) processStreams(streams []Stream, item *database.WatchlistItem) error {
	if len(streams) == 0 {
		return fmt.Errorf("no streams found")
	}

	s.log.Info("TorrentioScraper", "Scrape", fmt.Sprintf("Found %d total streams for %s", len(streams), item.Title))

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
		s.log.Info("TorrentioScraper", "Stream", "No streams found with size filter, showing all streams...")
		// Fall back to all streams
		filteredStreams = s.filterStreams(streams, item, false, false)
	}

	// Sort filtered streams by score
	sort.Slice(filteredStreams, func(i, j int) bool {
		return filteredStreams[i].Score > filteredStreams[j].Score
	})

	// Log results
	s.logResults(filteredStreams)

	// Save best match to database
	if len(filteredStreams) > 0 {
		bestMatch := filteredStreams[0]
		scrapeResult := &database.ScrapeResult{
			WatchlistItemID:   item.ID,
			ScrapedFilename:   sql.NullString{String: bestMatch.ParsedInfo.Title, Valid: true},
			ScrapedResolution: sql.NullString{String: bestMatch.ParsedInfo.Resolution, Valid: true},
			ScrapedDate:       sql.NullTime{Time: time.Now(), Valid: true},
			InfoHash:          sql.NullString{String: bestMatch.InfoHash, Valid: true},
			ScrapedScore:      sql.NullInt32{Int32: int32(bestMatch.Score), Valid: true},
			ScrapedFileSize:   sql.NullString{String: bestMatch.ParsedInfo.FileSize, Valid: true},
			ScrapedCodec:      sql.NullString{String: bestMatch.ParsedInfo.Codec, Valid: true},
			StatusResults:     sql.NullString{String: "ready_for_download", Valid: true},
		}

		_, err := s.db.SaveScrapeResult(scrapeResult)
		if err != nil {
			return fmt.Errorf("failed to save scrape result: %v", err)
		}

		s.log.Info("TorrentioScraper", "Database", fmt.Sprintf("Saved scrape result for %s: %s (Score: %d)", item.Title, scrapeResult.ScrapedFilename.String, scrapeResult.ScrapedScore.Int32))
	}

	return nil
}

// filterStreams applies the specified filters to the streams
func (s *TorrentioScraper) filterStreams(streams []Stream, item *database.WatchlistItem, useSize, useUploader bool) []Stream {
	var filtered []Stream

	// Get file size limits
	var minSize, maxSize float64
	if useSize {
		if item.MediaType.Valid && item.MediaType.String == "show" {
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

func (s *TorrentioScraper) saveScrapeResult(item *database.WatchlistItem, result *database.ScrapeResult) error {
	result.StatusResults = sql.NullString{String: "scraped", Valid: true} // Set status to "scraped"
	_, err := s.db.SaveScrapeResult(result)
	if err != nil {
		s.log.Error("TorrentioScraper", "saveScrapeResult", fmt.Sprintf("Failed to save scrape result: %v", err))
		return err
	}
	s.log.Info("TorrentioScraper", "Database", fmt.Sprintf("Saved scrape result for %s: %s (Score: %d)", item.Title, result.ScrapedFilename.String, result.ScrapedScore.Int32))
	return nil
}

func (s *TorrentioScraper) makeRequest(url string) (*http.Response, error) {
	scraperConfig := s.config.Scraping.Scrapers["torrentio"]
    
    // If rate limiting is enabled, ensure we wait between requests
    if scraperConfig.Ratelimit {
        // Wait at least 2 seconds between requests
        elapsed := time.Since(s.lastRequest)
        if elapsed < 2*time.Second {
            time.Sleep(2*time.Second - elapsed)
        }
    }

    // Make the request
    resp, err := s.client.Get(url)
    s.lastRequest = time.Now()

    // Handle rate limiting and server errors
    if resp != nil {
        if resp.StatusCode == 429 {
            // Wait longer on rate limit (double the configured timeout)
            retryWait := time.Duration(scraperConfig.Timeout*2) * time.Second
            if retryWait < 5*time.Second {
                retryWait = 5 * time.Second // Minimum 5 seconds
            }
            
            s.log.Warning("TorrentioScraper", "makeRequest", 
                fmt.Sprintf("Rate limited, waiting %v before retry", retryWait))
            
            time.Sleep(retryWait)
            resp, err = s.client.Get(url)
            s.lastRequest = time.Now()
        } else if resp.StatusCode >= 500 {
            // Server error, wait a bit and retry once
            retryWait := 5 * time.Second
            s.log.Warning("TorrentioScraper", "makeRequest", 
                fmt.Sprintf("Server error %d, waiting %v before retry", resp.StatusCode, retryWait))
            
            time.Sleep(retryWait)
            resp, err = s.client.Get(url)
            s.lastRequest = time.Now()
        }
    }

    return resp, err
}

func (s *TorrentioScraper) searchTorrentio(item *database.WatchlistItem, query string) (*TorrentioResponse, error) {
	var urls []string

	if item.ImdbID.Valid && item.ImdbID.String != "" {
		urls = append(urls, fmt.Sprintf("%s/stream/series/%s:%s.json",
			s.config.Scraping.Scrapers["torrentio"].URL, item.ImdbID.String, query))
	}

	if len(urls) == 0 {
		return nil, fmt.Errorf("no valid ID found for item")
	}

	var lastErr error
	var allStreams []Stream

	for _, url := range urls {
		s.log.Info("TorrentioScraper", "searchTorrentio",
			fmt.Sprintf("Trying URL for %s %s: %s", item.Title, query, url))

		resp, err := s.makeRequest(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			s.log.Warning("TorrentioScraper", "Scrape", 
				fmt.Sprintf("unexpected status code: %d for URL %s", resp.StatusCode, url))
			lastErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			continue
		}

		var response TorrentioResponse
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			lastErr = err
			continue
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

	if lastErr != nil {
		return nil, fmt.Errorf("failed to search torrentio: %v", lastErr)
	}

	return nil, fmt.Errorf("no streams found")
}
