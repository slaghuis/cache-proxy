package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	Requests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_proxy_requests_total",
			Help: "Total chat completion requests.",
		},
		[]string{"model", "cache", "escalated", "tag"},
	)

	Latency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_proxy_request_duration_seconds",
			Help:    "End-to-end request latency.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"model", "cache", "escalated"},
	)

	LocalScore = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_proxy_local_score",
			Help:    "Scoring result for local-model attempts.",
			Buckets: []float64{0.1, 0.3, 0.5, 0.65, 0.75, 0.85, 0.95, 1.0},
		},
		[]string{"tag"},
	)

	LocalLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_proxy_local_duration_seconds",
			Help:    "Local-model call duration (before escalation decision).",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30},
		},
		[]string{"model"},
	)

	Tokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_proxy_tokens_total",
			Help: "Prompt and completion tokens consumed.",
		},
		[]string{"model", "kind"}, // kind=prompt|completion
	)

	CostUSD = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_proxy_cost_usd_total",
			Help: "USD cost incurred (0 for local / cached).",
		},
		[]string{"model", "tag"},
	)

	SavedUSDEstimate = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_proxy_saved_usd_total",
			Help: "Estimated USD saved by cache hits and local-pass.",
		},
		[]string{"reason"}, // reason=cache_exact|cache_semantic|local_pass
	)
)

func init() {
	prometheus.MustRegister(
		Requests, Latency, LocalScore, LocalLatency,
		Tokens, CostUSD, SavedUSDEstimate,
	)
}

func Handler() http.Handler {
	return promhttp.Handler()
}

// Record is called from the chat handler after each completed request.
type RecordParams struct {
	Model       string
	Cache       string // "", "exact", "semantic"
	Escalated   bool
	Tag         string
	PromptTok   int
	OutputTok   int
	CostUSD     float64
	SavedUSD    float64
	SavedReason string // populated when SavedUSD > 0
	Latency     time.Duration
	LocalScore  float64 // 0 if not applicable
	LocalDur    time.Duration
	LocalModel  string
}

func Record(p RecordParams) {
	escalated := strconv.FormatBool(p.Escalated)
	Requests.WithLabelValues(p.Model, p.Cache, escalated, p.Tag).Inc()
	Latency.WithLabelValues(p.Model, p.Cache, escalated).Observe(p.Latency.Seconds())
	Tokens.WithLabelValues(p.Model, "prompt").Add(float64(p.PromptTok))
	Tokens.WithLabelValues(p.Model, "completion").Add(float64(p.OutputTok))
	CostUSD.WithLabelValues(p.Model, p.Tag).Add(p.CostUSD)
	if p.SavedUSD > 0 && p.SavedReason != "" {
		SavedUSDEstimate.WithLabelValues(p.SavedReason).Add(p.SavedUSD)
	}
	if p.LocalScore > 0 {
		LocalScore.WithLabelValues(p.Tag).Observe(p.LocalScore)
	}
	if p.LocalDur > 0 && p.LocalModel != "" {
		LocalLatency.WithLabelValues(p.LocalModel).Observe(p.LocalDur.Seconds())
	}
}