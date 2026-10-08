package costs

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/slaghuis/cache-proxy/internal/config"
)

type Ledger struct{ db *sql.DB }

func New(path string) (*Ledger, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS calls (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          INTEGER NOT NULL,
		model       TEXT    NOT NULL,
		prompt_tok  INTEGER NOT NULL,
		output_tok  INTEGER NOT NULL,
		cost_usd    REAL    NOT NULL,
		cached      TEXT    NOT NULL,  -- "exact" | "semantic" | ""
		latency_ms  INTEGER NOT NULL,
		tag         TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);
	CREATE INDEX IF NOT EXISTS idx_calls_model ON calls(model);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Ledger{db: db}, nil
}

func (l *Ledger) Record(cfg *config.Config, model string, in, out int,
	cached string, latency time.Duration, tag string) error {

	cost := 0.0
	if cached == "" {
		if p, ok := cfg.Pricing[model]; ok {
			cost = float64(in)/1_000_000*p.InputPer1M +
				float64(out)/1_000_000*p.OutputPer1M
		}
	}
	_, err := l.db.Exec(
		`INSERT INTO calls(ts, model, prompt_tok, output_tok, cost_usd, cached, latency_ms, tag)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), model, in, out, cost, cached, latency.Milliseconds(), tag,
	)
	return err
}

type Stats struct {
	TotalCalls   int     `json:"total_calls"`
	CacheHits    int     `json:"cache_hits"`
	CostUSD      float64 `json:"cost_usd"`
	SavedUSDEst  float64 `json:"saved_usd_estimate"`
	HitRate      float64 `json:"hit_rate"`
}

func (l *Ledger) Stats(sinceHours int) (*Stats, error) {
	since := time.Now().Add(-time.Duration(sinceHours) * time.Hour).Unix()
	s := &Stats{}
	row := l.db.QueryRow(
		`SELECT COUNT(*), 
		        SUM(CASE WHEN cached <> '' THEN 1 ELSE 0 END),
		        COALESCE(SUM(cost_usd), 0)
		 FROM calls WHERE ts >= ?`, since)
	if err := row.Scan(&s.TotalCalls, &s.CacheHits, &s.CostUSD); err != nil {
		return nil, err
	}
	if s.TotalCalls > 0 {
		s.HitRate = float64(s.CacheHits) / float64(s.TotalCalls)
	}
	// Estimated saved: hits × avg cost of a miss
	if s.TotalCalls > s.CacheHits && (s.TotalCalls-s.CacheHits) > 0 {
		avgMiss := s.CostUSD / float64(s.TotalCalls-s.CacheHits)
		s.SavedUSDEst = avgMiss * float64(s.CacheHits)
	}
	return s, nil
}