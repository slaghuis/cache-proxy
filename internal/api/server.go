package api

import (
	"log/slog"
	"net/http"

	"github.com/slaghuis/cache-proxy/internal/cache"
	"github.com/slaghuis/cache-proxy/internal/config"
	"github.com/slaghuis/cache-proxy/internal/costs"
	"github.com/slaghuis/cache-proxy/internal/embedder"
	"github.com/slaghuis/cache-proxy/internal/upstream"
)

type Server struct {
	Mux *http.ServeMux
}

func NewServer(cfg *config.Config, up *upstream.Client, ex *cache.Exact,
	sem *cache.Semantic, emb *embedder.Ollama, l *costs.Ledger, log *slog.Logger) *Server {

	mux := http.NewServeMux()
	chat := &ChatHandler{Up: up, Exact: ex, Semantic: sem, Embedder: emb, Ledger: l, Log: log}
	mux.Handle("/v1/chat/completions", chat)

	// Pass-through for /v1/models so clients can introspect
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", up.BaseURL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+up.APIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = copyBody(w, resp.Body)
	})

	mux.HandleFunc("/admin/stats", adminStats(l))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return &Server{Mux: mux}
}