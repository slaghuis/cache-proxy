package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/slaghuis/cache-proxy/internal/api"
	"github.com/slaghuis/cache-proxy/internal/cache"
	"github.com/slaghuis/cache-proxy/internal/config"
	"github.com/slaghuis/cache-proxy/internal/costs"
	"github.com/slaghuis/cache-proxy/internal/embedder"
	"github.com/slaghuis/cache-proxy/internal/upstream"
	"github.com/slaghuis/cache-proxy/internal/escalator"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "config path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if _, err := config.Load(*cfgPath); err != nil {
				logger.Warn("reload failed", "err", err)
			} else {
				logger.Info("config reloaded")
			}
		}
	}()

	emb := embedder.NewOllama(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	up := upstream.New(cfg.Upstream.BaseURL, cfg.Upstream.APIKey)

	ex, err := cache.NewExact(cfg.Cache.SQLitePath, cfg.Cache.TTLHours)
	if err != nil {
		log.Fatalf("exact cache: %v", err)
	}
	sem, err := cache.NewSemantic(cfg.Qdrant.Host, cfg.Qdrant.Port,
		cfg.Qdrant.Collection, cfg.Cache.SemanticThreshold, cfg.Cache.TTLHours)
	if err != nil {
		log.Fatalf("semantic cache: %v", err)
	}
	ledger, err := costs.New(cfg.Cache.SQLitePath)
	if err != nil {
		log.Fatalf("ledger: %v", err)
	}

    	esc := &escalator.Escalator{
        	Cfg:      cfg,
        	Up:       up,
        	Embedder: emb,       // reuse the prompt-embedding model
        	Log:      logger,
    	}

    	srv := api.NewServer(cfg, up, ex, sem, emb, ledger, esc, logger)
	logger.Info("cache-proxy starting", "listen", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, srv.Mux); err != nil {
		log.Fatal(err)
	}
}
