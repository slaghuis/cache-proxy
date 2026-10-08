package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/slaghuis/cache-proxy/internal/costs"
)

func adminStats(l *costs.Ledger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours == 0 {
			hours = 24
		}
		s, err := l.Stats(hours)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	}
}

func copyBody(w http.ResponseWriter, r io.Reader) (int64, error) {
	return io.Copy(w, r)
}