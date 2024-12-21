package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Fetchers        map[string]FetcherConfig `yaml:"fetchers"`
	Scraping        ScrapingConfig           `yaml:"scraping"`
	Database        DatabaseConfig           `yaml:"database"`
	RabbitMQ        RabbitMQConfig           `yaml:"rabbitmq"`
	General         GeneralConfig            `yaml:"general"`
	CustomLibraries []CustomLibrary          `yaml:"custom_libraries"`
	DebridAPI       string                   `yaml:"debrid_api"`
	RealDebridToken string                   `yaml:"real_debrid_token"`
	Programs        ProgramsConfig           `yaml:"programs"`
	TMDB            TMDB                     `yaml:"tmdb"`
	ProcessManagement ProcessManagementConfig `yaml:"process_management"`
}

type FetcherConfig struct {
	Enabled  bool     `yaml:"enabled"`
	URLs     []string `yaml:"urls"`
	Interval int      `yaml:"interval"`
}

type DatabaseConfig struct {
	URL string `yaml:"url"`
}

type RabbitMQConfig struct {
	Host  string `yaml:"host"`
	Queue string `yaml:"queue"`
}

type GeneralConfig struct {
	LibraryPath                     string `yaml:"library_path"`
	ProcessAutomatically            bool   `yaml:"process_automatically"`
	NumberOfFilesToProcessByProgram int    `yaml:"number_of_files_to_process_by_program"`
	Timeout                         int    `yaml:"timeout"`
	MaxRetries                      int    `yaml:"max_retries"`
	RclonePath                      string `yaml:"rclone_path"`
}

type CustomLibrary struct {
	Name                   string  `yaml:"name"`
	Path                   string  `yaml:"path"`
	Active                 bool    `yaml:"active"`
	DuplicateInMainLibrary bool    `yaml:"duplicate_in_main_library"`
	Filters                Filters `yaml:"filters"`
}

type Filters struct {
	Include []Filter `yaml:"include"`
	Exclude []Filter `yaml:"exclude"`
}

type Filter struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

type ScrapingConfig struct {
	Scrapers           map[string]ScraperConfig `yaml:"scrapers"`
	Filesize           FilesizeConfig           `yaml:"filesize"`
	PreferredUploaders []string                 `yaml:"preferredUploaders"`
	Languages          LanguagesConfig          `yaml:"languages"`
	Ranking            RankingConfig            `yaml:"ranking"`
}

type ScraperConfig struct {
	Enabled              bool          `yaml:"enabled"`
	Priority             int           `yaml:"priority"`
	ScraperGroup         int           `yaml:"scraper_group"`
	OnlyForCustomLibrary []string      `yaml:"only_for_custom_library"`
	Filter               string        `yaml:"filter"`
	URL                  string        `yaml:"url"`
	Timeout              int           `yaml:"timeout"`
	Ratelimit            bool          `yaml:"ratelimit"`
	Scoring              ScoringConfig `yaml:"scoring"`
}

type ScoringConfig struct {
	LanguageIncludeScore   int            `yaml:"languageIncludeScore"`
	LanguageExcludePenalty int            `yaml:"languageExcludePenalty"`
	ResolutionScores       map[string]int `yaml:"resolutionScores"`
	QualityScores          map[string]int `yaml:"qualityScores"`
	MaxSeederScore         int            `yaml:"maxSeederScore"`
	MaxSizeScore           int            `yaml:"maxSizeScore"`
	CodecScores            map[string]int `yaml:"codecScores"`
	PreferredUploaderScore int            `yaml:"preferredUploaderScore"`
}

type RankingConfig struct {
	BingeGroupPriority      []map[string][]string `yaml:"bingeGroupPriority"`
	MaxResultsPerResolution int                   `yaml:"maxResultsPerResolution"`
	Scoring                 ScoringConfig         `yaml:"scoring"`
}

type ProgramsConfig struct {
	ContentFetcher  ProgramStatus `yaml:"content_fetcher"`
	Scraper        ProgramStatus `yaml:"scraper"`
	Downloader     ProgramStatus `yaml:"downloader"`
	LibraryMatcher ProgramStatus `yaml:"library_matcher"`
	Symlinker      ProgramStatus `yaml:"symlinker"`
}

type ProgramStatus struct {
	Active        bool          `yaml:"active"`
	Priority      int           `yaml:"priority"`
	CheckInterval time.Duration `yaml:"check_interval"`
	MaxRetries    int           `yaml:"max_retries"`
	Repair        *RepairConfig `yaml:"repair,omitempty"`
}

type RepairConfig struct {
	Enabled         bool          `yaml:"enabled"`
	CheckInterval   time.Duration `yaml:"check_interval"`
	BatchSize       int           `yaml:"batch_size"`
	BatchDelay      time.Duration `yaml:"batch_delay"`
	MaxErrorsPerItem int          `yaml:"max_errors_per_item"`
}

type ProcessManagementConfig struct {
	DefaultRetryWaitTime time.Duration `yaml:"default_retry_wait_time"`
	DefaultMaxRetries    int           `yaml:"default_max_retries"`
}

type TMDB struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

type FilesizeConfig struct {
	Movie MovieFilesize `yaml:"movie"`
	Show  ShowFilesize  `yaml:"show"`
}

type MovieFilesize struct {
	SizeUnit string  `yaml:"size_unit"`
	Min      float64 `yaml:"min"`
	Max      float64 `yaml:"max"`
}

type ShowFilesize struct {
	SizeUnit string  `yaml:"size_unit"`
	Min      float64 `yaml:"min"`
	Max      float64 `yaml:"max"`
}

type LanguagesConfig struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// LoadConfig loads the configuration from a file and environment variables
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("error parsing config file: %v", err)
	}

	// Override with environment variables
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cfg.Database.URL = dbURL
	}

	if debridAPI := os.Getenv("DEBRID_API_KEY"); debridAPI != "" {
		cfg.DebridAPI = debridAPI
	}

	if realDebridToken := os.Getenv("REAL_DEBRID_TOKEN"); realDebridToken != "" {
		cfg.RealDebridToken = realDebridToken
	}

	cfg.TMDB.APIKey = os.Getenv("TMDB_API_KEY")

	// Add other environment variable overrides as needed...

	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %v", err)
	}

	return &cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate scraping configuration
	if err := c.validateScrapingConfig(); err != nil {
		return fmt.Errorf("invalid scraping config: %v", err)
	}

	return nil
}

func (c *Config) validateScrapingConfig() error {
	// Validate filesize configuration
	if err := c.validateFilesizeConfig(); err != nil {
		return fmt.Errorf("invalid filesize config: %v", err)
	}

	// Validate scrapers configuration
	for name, scraper := range c.Scraping.Scrapers {
		if err := validateScraperConfig(name, scraper); err != nil {
			return fmt.Errorf("invalid scraper config for %s: %v", name, err)
		}
	}

	return nil
}

func (c *Config) validateFilesizeConfig() error {
	filesize := c.Scraping.Filesize

	// Validate movie filesize config
	if filesize.Movie.SizeUnit != "GB" {
		return fmt.Errorf("movie size_unit must be 'GB', got '%s'", filesize.Movie.SizeUnit)
	}
	if filesize.Movie.Min <= 0 {
		return fmt.Errorf("movie min size must be greater than 0, got %f", filesize.Movie.Min)
	}
	if filesize.Movie.Max <= filesize.Movie.Min {
		return fmt.Errorf("movie max size (%f) must be greater than min size (%f)",
			filesize.Movie.Max, filesize.Movie.Min)
	}

	// Validate show filesize config
	if filesize.Show.SizeUnit != "GB" {
		return fmt.Errorf("show size_unit must be 'GB', got '%s'", filesize.Show.SizeUnit)
	}
	if filesize.Show.Min <= 0 {
		return fmt.Errorf("show min size must be greater than 0, got %f", filesize.Show.Min)
	}
	if filesize.Show.Max <= filesize.Show.Min {
		return fmt.Errorf("show max size (%f) must be greater than min size (%f)",
			filesize.Show.Max, filesize.Show.Min)
	}

	return nil
}

func validateScraperConfig(name string, config ScraperConfig) error {
	// Validate basic scraper configuration
	if !config.Enabled {
		return nil // Skip validation for disabled scrapers
	}

	if name == "" {
		return fmt.Errorf("scraper name cannot be empty")
	}

	if config.URL == "" {
		return fmt.Errorf("scraper %s: URL is required", name)
	}

	if config.Timeout <= 0 {
		return fmt.Errorf("scraper %s: timeout must be greater than 0", name)
	}

	if config.Priority <= 0 {
		return fmt.Errorf("scraper %s: priority must be greater than 0", name)
	}

	if config.ScraperGroup <= 0 {
		return fmt.Errorf("scraper %s: scraper_group must be greater than 0", name)
	}

	// Validate scoring configuration if present
	if err := validateScoringConfig(config.Scoring); err != nil {
		return fmt.Errorf("scraper %s: invalid scoring config: %v", name, err)
	}

	return nil
}

func validateScoringConfig(config ScoringConfig) error {
	// Validate resolution scoring
	if config.ResolutionScores["2160p"] < 0 || config.ResolutionScores["1080p"] < 0 ||
		config.ResolutionScores["720p"] < 0 || config.ResolutionScores["480p"] < 0 {
		return fmt.Errorf("resolution scores cannot be negative")
	}

	// Validate codec scoring
	if config.CodecScores["hevc"] < 0 || config.CodecScores["avc"] < 0 {
		return fmt.Errorf("codec scores cannot be negative")
	}

	// Validate seeds multiplier
	if config.MaxSeederScore < 0 {
		return fmt.Errorf("maxSeederScore cannot be negative")
	}

	return nil
}
