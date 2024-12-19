-- Create sequences
CREATE SEQUENCE IF NOT EXISTS watchlistitem_id_seq;
CREATE SEQUENCE IF NOT EXISTS seasons_id_seq;
CREATE SEQUENCE IF NOT EXISTS tv_episodes_id_seq;
CREATE SEQUENCE IF NOT EXISTS scrape_results_id_seq;

-- Table: public.watchlistitem
CREATE TABLE IF NOT EXISTS public.watchlistitem
(
    id integer NOT NULL DEFAULT nextval('watchlistitem_id_seq'::regclass),
    title character varying(255) COLLATE pg_catalog."default" NOT NULL,
    item_year integer,
    requested_date timestamp without time zone NOT NULL,
    link text COLLATE pg_catalog."default",
    imdb_id character varying(20) COLLATE pg_catalog."default",
    tmdb_id character varying(20) COLLATE pg_catalog."default",
    tvdb_id character varying(20) COLLATE pg_catalog."default",
    description text COLLATE pg_catalog."default",
    category character varying(50) COLLATE pg_catalog."default",
    genres text COLLATE pg_catalog."default",
    rating character varying(10) COLLATE pg_catalog."default",
    status character varying(20) COLLATE pg_catalog."default",
    thumbnail_url text COLLATE pg_catalog."default",
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    best_scraped_filename text COLLATE pg_catalog."default",
    best_scraped_resolution character varying(20) COLLATE pg_catalog."default",
    last_scraped_date timestamp without time zone,
    custom_library character varying(100) COLLATE pg_catalog."default",
    main_library_path boolean,
    best_scraped_score integer,
    media_type character varying(10) COLLATE pg_catalog."default",
    total_seasons integer,
    total_episodes integer,
    release_date date,
    retry_count integer DEFAULT 0,
    show_status character varying(255) COLLATE pg_catalog."default",
    current_step character varying(200) COLLATE pg_catalog."default",
    CONSTRAINT watchlistitem_pkey PRIMARY KEY (id)
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.watchlistitem
    OWNER to postgres;

COMMENT ON COLUMN public.watchlistitem.status
    IS 'Overall status of the watchlist item';

-- Table: public.seasons
CREATE TABLE IF NOT EXISTS public.seasons
(
    id integer NOT NULL DEFAULT nextval('seasons_id_seq'::regclass),
    watchlist_item_id integer NOT NULL,
    season_number integer NOT NULL,
    air_date date,
    overview text COLLATE pg_catalog."default",
    poster_path text COLLATE pg_catalog."default",
    episode_count integer,
    CONSTRAINT seasons_pkey PRIMARY KEY (id),
    CONSTRAINT seasons_watchlist_item_id_fkey FOREIGN KEY (watchlist_item_id)
        REFERENCES public.watchlistitem (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.seasons
    OWNER to postgres;

-- Table: public.tv_episodes
CREATE TABLE IF NOT EXISTS public.tv_episodes
(
    id integer NOT NULL DEFAULT nextval('tv_episodes_id_seq'::regclass),
    season_id integer NOT NULL,
    episode_number integer NOT NULL,
    episode_name text COLLATE pg_catalog."default",
    air_date date,
    overview text COLLATE pg_catalog."default",
    still_path text COLLATE pg_catalog."default",
    scraped boolean DEFAULT false,
    scrape_result_id integer,
    CONSTRAINT tv_episodes_pkey PRIMARY KEY (id),
    CONSTRAINT tv_episodes_season_id_episode_number_key UNIQUE (season_id, episode_number),
    CONSTRAINT tv_episodes_season_id_fkey FOREIGN KEY (season_id)
        REFERENCES public.seasons (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.tv_episodes
    OWNER to postgres;

-- Table: public.scrape_results
CREATE TABLE IF NOT EXISTS public.scrape_results
(
    id integer NOT NULL DEFAULT nextval('scrape_results_id_seq'::regclass),
    watchlist_item_id integer,
    scraped_filename text COLLATE pg_catalog."default",
    scraped_resolution text COLLATE pg_catalog."default",
    scraped_date timestamp without time zone,
    info_hash text COLLATE pg_catalog."default",
    debrid_id text COLLATE pg_catalog."default",
    debrid_uri text COLLATE pg_catalog."default",
    scraped_score integer,
    scraped_file_size text COLLATE pg_catalog."default",
    scraped_codec text COLLATE pg_catalog."default",
    status_results text COLLATE pg_catalog."default",
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT scrape_results_pkey PRIMARY KEY (id),
    CONSTRAINT fk_watchlist_item FOREIGN KEY (watchlist_item_id)
        REFERENCES public.watchlistitem (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE CASCADE,
    CONSTRAINT scrape_results_watchlist_item_id_fkey FOREIGN KEY (watchlist_item_id)
        REFERENCES public.watchlistitem (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION
)
TABLESPACE pg_default;

ALTER TABLE IF EXISTS public.scrape_results
    OWNER to postgres;

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_scrape_results_status
    ON public.scrape_results USING btree
    (status_results COLLATE pg_catalog."default" ASC NULLS LAST)
    TABLESPACE pg_default;

CREATE INDEX IF NOT EXISTS idx_scrape_results_watchlist_item_id
    ON public.scrape_results USING btree
    (watchlist_item_id ASC NULLS LAST)
    TABLESPACE pg_default;
