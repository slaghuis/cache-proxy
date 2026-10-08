package cache

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/slaghuis/cache-proxy/internal/api"
)

type Exact struct {
	db       *sql.DB
	ttlHours int
}

func NewExact(path string, ttlHours int) (*Exact, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS exact_cache (
		key        TEXT PRIMARY KEY,
		model      TEXT NOT NULL,
		response   TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_exact_created ON exact_cache(created_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Exact{db: db, ttlHours: ttlHours}, nil
}

func (e *Exact) Get(key string) (*api.ChatResponse, bool) {
	var payload string
	var created int64
	row := e.db.QueryRow(
		`SELECT response, created_at FROM exact_cache WHERE key = ?`, key)
	if err := row.Scan(&payload, &created); err != nil {
		return nil, false
	}
	if time.Since(time.Unix(created, 0)) > time.Duration(e.ttlHours)*time.Hour {
		_, _ = e.db.Exec(`DELETE FROM exact_cache WHERE key = ?`, key)
		return nil, false
	}
	var resp api.ChatResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		return nil, false
	}
	return &resp, true
}

func (e *Exact) Put(key, model string, resp *api.ChatResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = e.db.Exec(
		`INSERT OR REPLACE INTO exact_cache(key, model, response, created_at)
		 VALUES(?, ?, ?, ?)`,
		key, model, string(b), time.Now().Unix(),
	)
	return err
}