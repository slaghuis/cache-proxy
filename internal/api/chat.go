package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/slaghuis/cache-proxy/internal/cache"
	"github.com/slaghuis/cache-proxy/internal/config"
	"github.com/slaghuis/cache-proxy/internal/costs"
	"github.com/slaghuis/cache-proxy/internal/embedder"
	"github.com/slaghuis/cache-proxy/internal/escalator"
	"github.com/slaghuis/cache-proxy/internal/metrics"
	"github.com/slaghuis/cache-proxy/internal/router"
	"github.com/slaghuis/cache-proxy/internal/upstream"
)

type ChatHandler struct {
	Up        *upstream.Client
	Exact     *cache.Exact
	Semantic  *cache.Semantic
	Embedder  *embedder.Ollama
	Ledger    *costs.Ledger
	Escalator *escalator.Escalator
	Log       *slog.Logger
}

func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cfg := config.Current()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	tag := r.Header.Get("x-task-tag")

	// -------------------------------------------------------------------
	// Step 1: Static routing (cheap, pure).
	// Determines which model the request would be sent to if the cache
	// misses AND we go via the legacy direct-forward path.
	// Also provides the model key for cache lookups.
	// -------------------------------------------------------------------
	targetModel := router.Route(cfg, &req, r.Header)

	h.Log.Info("chat",
		"requested", req.Model, "routed_to", targetModel,
		"stream", req.Stream, "tag", tag, "msgs", len(req.Messages))

	// -------------------------------------------------------------------
	// Step 2: Cache lookup, keyed by targetModel.
	// Only runs if the request is cacheable (no tools, low temperature).
	// -------------------------------------------------------------------
	if cfg.Cache.Enabled && cache.Cacheable(&req) {
		exactKey, semText := cache.Normalize(&req)

		// 2a. Exact hit — identical normalized request already seen.
		if resp, ok := h.Exact.Get(exactKey); ok {
			h.Log.Info("exact hit", "key", exactKey[:12], "model", targetModel)
			resp.Cached = "exact"
			h.respond(w, &req, resp, "exact", targetModel, start, tag, 0)
			return
		}

		// 2b. Semantic hit — close-enough prompt for the same model.
		if len(semText) <= cfg.Cache.MaxPromptChars {
			vec, err := h.Embedder.Embed(r.Context(), semText)
			if err == nil {
				resp, score, ok := h.Semantic.Lookup(r.Context(), vec, targetModel)
				if ok {
					h.Log.Info("semantic hit",
						"score", score, "model", targetModel)
					resp.Cached = "semantic"
					resp.CacheScore = score
					// Promote to exact cache so next identical call is instant.
					_ = h.Exact.Put(exactKey, targetModel, resp)
					h.respond(w, &req, resp, "semantic", targetModel, start, tag, score)
					return
				}
			} else {
				h.Log.Warn("embed for lookup failed", "err", err)
			}
		}
	}

	// -------------------------------------------------------------------
	// Step 3: Cache miss. Choose execution path.
	// Escalation path handles unary, tool-less, auto/escalate-always modes.
	// Everything else uses legacy direct forwarding.
	// -------------------------------------------------------------------
	policy := escalator.ResolvePolicy(cfg, r.Header)

	useEscalation := cfg.Escalation.Enabled &&
		h.Escalator != nil &&
		!req.Stream && // streaming through the escalator is deliberately not supported
		len(req.Tools) == 0 && // tool-use needs raw passthrough
		policy.Mode != escalator.ModeLocal // local-only is a one-shot local call, handled below

	if useEscalation {
		h.handleWithEscalation(w, r, &req, raw, policy, tag, start)
		return
	}

	// -------------------------------------------------------------------
	// Step 4: Legacy direct forwarding (streaming, tool-use, local-only).
	// -------------------------------------------------------------------
	// If local-only was requested, swap targetModel for the profile's local
	// so the request actually goes to Ollama via LiteLLM.
	if policy.Mode == escalator.ModeLocal && policy.Profile.LocalModel != "" {
		targetModel = policy.Profile.LocalModel
	}

	req.Model = targetModel
	fwdBody, _ := injectModel(raw, targetModel)

	if req.Stream {
		h.proxyStream(w, r, &req, fwdBody, targetModel, start, tag)
	} else {
		h.proxyUnary(w, r, &req, fwdBody, targetModel, start, tag)
	}
}

// handleWithEscalation runs the local-first attempt, scores it, and either
// serves the local response or escalates to the cloud model.
func (h *ChatHandler) handleWithEscalation(w http.ResponseWriter, r *http.Request,
	req *ChatRequest, raw []byte, policy escalator.Policy, tag string, start time.Time) {

	cfg := config.Current()
	promptText := extractPromptText(req)

	localBody, err := escalator.InjectModel(raw, policy.Profile.LocalModel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cloudBody, err := escalator.InjectModel(raw, policy.Profile.CloudModel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := h.Escalator.Run(r.Context(), policy, promptText, localBody, cloudBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Emit response + diagnostic headers.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-model-used", result.ModelUsed)
	w.Header().Set("x-escalated", fmt.Sprintf("%v", result.Escalated))
	w.Header().Set("x-score", fmt.Sprintf("%.2f", result.Score))
	if result.Escalated {
		w.Header().Set("x-escalation-reason", result.Reason)
	}
	_, _ = w.Write(result.ResponseBody)

	// Parse response for cost accounting and cache storage.
	var resp ChatResponse
	_ = json.Unmarshal(result.ResponseBody, &resp)

	cost := computeCost(cfg, result.ModelUsed, resp.Usage)
	_ = h.Ledger.RecordEscalated(cfg, result.ModelUsed,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		"", time.Since(start), tag, result.Escalated,
		result.Score, result.Reason)

	// Live Prometheus metrics.
	saved, savedReason := 0.0, ""
	if !result.Escalated {
		// Local pass: we avoided a cloud call we would otherwise have made.
		saved = computeCost(cfg, policy.Profile.CloudModel, resp.Usage)
		savedReason = "local_pass"
	}
	metrics.Record(metrics.RecordParams{
		Model:       result.ModelUsed,
		Cache:       "",
		Escalated:   result.Escalated,
		Tag:         tag,
		PromptTok:   resp.Usage.PromptTokens,
		OutputTok:   resp.Usage.CompletionTokens,
		CostUSD:     cost,
		SavedUSD:    saved,
		SavedReason: savedReason,
		Latency:     time.Since(start),
		LocalScore:  result.Score,
		LocalDur:    result.LocalLatency,
		LocalModel:  policy.Profile.LocalModel,
	})

	// Cache the response under the model that actually produced it.
	if cfg.Cache.Enabled && cache.Cacheable(req) {
		go h.storeInCache(req, &resp, result.ModelUsed)
	}
}

// -------------------------------------------------------------------
// Legacy forwarding paths (unchanged from pre-escalator behaviour).
// -------------------------------------------------------------------

func (h *ChatHandler) proxyUnary(w http.ResponseWriter, r *http.Request,
	req *ChatRequest, body []byte, model string, start time.Time, tag string) {

	cfg := config.Current()
	respBody, status, err := h.Up.Call(r.Context(), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)

	if status != http.StatusOK {
		return
	}
	var resp ChatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return
	}
	cost := computeCost(cfg, model, resp.Usage)
	_ = h.Ledger.Record(cfg, model, resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens, "", time.Since(start), tag)

	metrics.Record(metrics.RecordParams{
		Model:     model,
		Cache:     "",
		Escalated: false,
		Tag:       tag,
		PromptTok: resp.Usage.PromptTokens,
		OutputTok: resp.Usage.CompletionTokens,
		CostUSD:   cost,
		Latency:   time.Since(start),
	})

	if cfg.Cache.Enabled && cache.Cacheable(req) {
		go h.storeInCache(req, &resp, model)
	}
}

func (h *ChatHandler) proxyStream(w http.ResponseWriter, r *http.Request,
	req *ChatRequest, body []byte, model string, start time.Time, tag string) {

	cfg := config.Current()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	assembled, upstreamModel, err := h.Up.Stream(r.Context(), body, func(chunk []byte) error {
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		h.Log.Warn("stream err", "err", err)
		return
	}

	in := estimateTokens(req)
	out := estimateTokensFromString(assembled)
	_ = h.Ledger.Record(cfg, model, in, out, "", time.Since(start), tag)

	metrics.Record(metrics.RecordParams{
		Model:     model,
		Cache:     "",
		Escalated: false,
		Tag:       tag,
		PromptTok: in,
		OutputTok: out,
		CostUSD:   computeCost(cfg, model, Usage{PromptTokens: in, CompletionTokens: out}),
		Latency:   time.Since(start),
	})

	if cfg.Cache.Enabled && cache.Cacheable(req) {
		resp := &ChatResponse{
			Model: upstreamModel,
			Choices: []Choice{{
				Message:      ResponseMessage{Role: "assistant", Content: assembled},
				FinishReason: "stop",
			}},
			Usage: Usage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out},
		}
		go h.storeInCache(req, resp, model)
	}
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

func (h *ChatHandler) storeInCache(req *ChatRequest, resp *ChatResponse, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exactKey, semText := cache.Normalize(req)
	_ = h.Exact.Put(exactKey, model, resp)

	cfg := config.Current()
	if len(semText) == 0 || len(semText) > cfg.Cache.MaxPromptChars {
		return
	}
	vec, err := h.Embedder.Embed(ctx, semText)
	if err != nil {
		h.Log.Warn("embed on store", "err", err)
		return
	}
	if err := h.Semantic.Store(ctx, vec, model, resp); err != nil {
		h.Log.Warn("semantic store", "err", err)
	}
}

func (h *ChatHandler) respond(w http.ResponseWriter, req *ChatRequest,
	resp *ChatResponse, hit, model string, start time.Time, tag string, score float32) {

	cfg := config.Current()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-cache", hit)
	w.Header().Set("x-model-used", model)

	if req.Stream {
		// Collapse the cached response into a single SSE chunk + [DONE].
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk := map[string]any{
			"id":     resp.ID,
			"object": "chat.completion.chunk",
			"model":  resp.Model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]string{
					"role":    "assistant",
					"content": resp.Choices[0].Message.Content,
				},
				"finish_reason": "stop",
			}},
			"x_cache":       hit,
			"x_cache_score": score,
		}
		b, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	} else {
		_ = json.NewEncoder(w).Encode(resp)
	}

	// Ledger: cached responses have zero cost.
	_ = h.Ledger.Record(cfg, model, resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens, hit, time.Since(start), tag)

	// Metrics: record the saving as what we would have paid.
	saved := computeCost(cfg, model, resp.Usage)
	metrics.Record(metrics.RecordParams{
		Model:       model,
		Cache:       hit,
		Escalated:   false,
		Tag:         tag,
		PromptTok:   resp.Usage.PromptTokens,
		OutputTok:   resp.Usage.CompletionTokens,
		CostUSD:     0,
		SavedUSD:    saved,
		SavedReason: "cache_" + hit,
		Latency:     time.Since(start),
	})
}

// injectModel rewrites the "model" field in a raw request body.
// Used only by the legacy direct-forward path.
func injectModel(raw []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["model"], _ = json.Marshal(model)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// extractPromptText pulls user/assistant content for prompt-aware scoring and
// embedding. Skips system messages (they're ambient context, not the query).
func extractPromptText(req *ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			b.WriteString(s)
			b.WriteString("\n")
			continue
		}
		// Multi-part content (array of {type,text}).
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &parts); err == nil {
			for _, p := range parts {
				if p.Type == "text" {
					b.WriteString(p.Text)
					b.WriteString("\n")
				}
			}
		}
	}
	return b.String()
}

func estimateTokens(req *ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content) / 4
	}
	return n
}

func estimateTokensFromString(s string) int {
	return len(s) / 4
}

func computeCost(cfg *config.Config, model string, u Usage) float64 {
	p, ok := cfg.Pricing[model]
	if !ok {
		return 0
	}
	return float64(u.PromptTokens)/1_000_000*p.InputPer1M +
		float64(u.CompletionTokens)/1_000_000*p.OutputPer1M
}
