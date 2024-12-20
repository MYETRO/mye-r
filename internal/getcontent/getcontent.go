package getcontent

import (
	"context"
	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/logger"
)

type Fetcher interface {
	Start(context.Context)
	Stop()
}

type GetContent struct {
	cfg      *config.Config
	db       *database.DB
	log      *logger.Logger
	fetchers map[string]Fetcher
}

func New(cfg *config.Config, db *database.DB) (*GetContent, error) {
	gc := &GetContent{
		cfg:      cfg,
		db:       db,
		log:      logger.New(),
		fetchers: make(map[string]Fetcher),
	}

	for name, fetcherConfig := range cfg.Fetchers {
		if fetcherConfig.Enabled {
			switch name {
			case "plexrss":
				gc.fetchers[name] = NewPlexRSSFetcher(cfg, db)
			default:
				gc.log.Warning("GetContent", "New", "Unknown fetcher type: "+name)
			}
		}
	}

	return gc, nil
}

func (gc *GetContent) Start(ctx context.Context) error {
	for name, fetcher := range gc.fetchers {
		go func(name string, f Fetcher) {
			gc.log.Info("GetContent", "Start", "Starting "+name+" fetcher")
			f.Start(ctx)
		}(name, fetcher)
	}

	return nil
}

func (gc *GetContent) Stop() error {
	for name, fetcher := range gc.fetchers {
		gc.log.Info("GetContent", "Stop", "Stopping "+name+" fetcher")
		fetcher.Stop()
	}
	return nil
}

func (gc *GetContent) Name() string {
    return "plexrss"
}

func (gc *GetContent) IsNeeded() bool {
    var count int
    err := gc.db.QueryRow(`
        SELECT COUNT(*) 
        FROM watchlistitem 
        WHERE status = 'new' 
        AND current_step = 'fetch_pending'
    `).Scan(&count)
    
    return err == nil && count > 0
}
