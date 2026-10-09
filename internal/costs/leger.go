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
        	cached      TEXT    NOT NULL,
        	latency_ms  INTEGER NOT NULL,
        	tag         TEXT,
        	escalated   INTEGER NOT NULL DEFAULT 0,
        	score       REAL    NOT NULL DEFAULT 0,
        	reason      TEXT
    	);
    	CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);
    	CREATE INDEX IF NOT EXISTS idx_calls_model ON calls(model);
    	CREATE INDEX IF NOT EXISTS idx_calls_escalated ON calls(escalated);
    `
    	// Idempotent migration:
    	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN escalated INTEGER NOT NULL DEFAULT 0`)
    	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN score REAL NOT NULL DEFAULT 0`)
    	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN reason TEXT`)

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

func (l *Ledger) RecordEscalated(cfg *config.Config, model string, in, out int,
    cached string, latency time.Duration, tag string,
    escalated bool, score float64, reason string) error {

    cost := 0.0
    if cached == "" {
        if p, ok := cfg.Pricing[model]; ok {
            cost = float64(in)/1_000_000*p.InputPer1M +
                float64(out)/1_000_000*p.OutputPer1M
        }
    }
    esc := 0
    if escalated {
        esc = 1
    }
    _, err := l.db.Exec(
        `INSERT INTO calls(ts,model,prompt_tok,output_tok,cost_usd,cached,latency_ms,tag,
                           escalated,score,reason)
         VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
        time.Now().Unix(), model, in, out, cost, cached,
        latency.Milliseconds(), tag, esc, score, reason)
    return err
}

type Stats struct {
    TotalCalls      int     `json:"total_calls"`
    CacheHits       int     `json:"cache_hits"`
    Escalations     int     `json:"escalations"`
    LocalSuccesses  int     `json:"local_successes"`
    CostUSD         float64 `json:"cost_usd"`
    SavedUSDEst     float64 `json:"saved_usd_estimate"`
    HitRate         float64 `json:"hit_rate"`
    LocalSuccessRate float64 `json:"local_success_rate"`
}

func (l *Ledger) Stats(sinceHours int) (*Stats, error) {
    since := time.Now().Add(-time.Duration(sinceHours) * time.Hour).Unix()
    s := &Stats{}
    row := l.db.QueryRow(
        `SELECT COUNT(*),
                SUM(CASE WHEN cached <> '' THEN 1 ELSE 0 END),
                SUM(CASE WHEN escalated = 1 THEN 1 ELSE 0 END),
                SUM(CASE WHEN cached = '' AND escalated = 0 AND score > 0 THEN 1 ELSE 0 END),
                COALESCE(SUM(cost_usd), 0)
         FROM calls WHERE ts >= ?`, since)
    if err := row.Scan(&s.TotalCalls, &s.CacheHits, &s.Escalations,
        &s.LocalSuccesses, &s.CostUSD); err != nil {
        return nil, err
    }
    if s.TotalCalls > 0 {
        s.HitRate = float64(s.CacheHits) / float64(s.TotalCalls)
    }
    escalationEligible := s.LocalSuccesses + s.Escalations
    if escalationEligible > 0 {
        s.LocalSuccessRate = float64(s.LocalSuccesses) / float64(escalationEligible)
    }
    if s.TotalCalls > s.CacheHits && (s.TotalCalls-s.CacheHits) > 0 {
        avgMiss := s.CostUSD / float64(s.TotalCalls-s.CacheHits)
        s.SavedUSDEst = avgMiss * float64(s.CacheHits+s.LocalSuccesses)
    }
    return s, nil
}
