package escalator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/slaghuis/cache-proxy/internal/config"
	"github.com/slaghuis/cache-proxy/internal/embedder"
	"github.com/slaghuis/cache-proxy/internal/upstream"
)

type Escalator struct {
	Cfg      *config.Config
	Up       *upstream.Client
	Embedder *embedder.Ollama // used for similarity signal
	Log      *slog.Logger
}

type Result struct {
	ResponseBody []byte
	ModelUsed    string
	Escalated    bool
	Reason       string
	Score        float64
	LocalLatency time.Duration
	CloudLatency time.Duration
	SignalReport Report
}

// Run executes the local-first escalation flow.
// bodyWithLocalModel must have "model" already set to the local model.
// bodyWithCloudModel is pre-built with the cloud model for fast fallback.
func (e *Escalator) Run(ctx context.Context, policy Policy, prompt string,
	bodyWithLocalModel, bodyWithCloudModel []byte) (*Result, error) {

	// Forced paths
	if policy.ForceCloud() {
		r, _, err := e.callUpstream(ctx, bodyWithCloudModel)
		return &Result{
			ResponseBody: r, ModelUsed: policy.Profile.CloudModel,
			Escalated: true, Reason: "forced by x-escalation header",
		}, err
	}
	if !policy.LocalEligible() {
		return nil, errors.New("local-only requested but not eligible")
	}

	// Local attempt with timeout
	localCtx, cancel := context.WithTimeout(ctx,
		time.Duration(e.Cfg.Escalation.LocalTimeoutSeconds)*time.Second)
	defer cancel()

	tLocal := time.Now()
	localResp, _, err := e.callUpstream(localCtx, bodyWithLocalModel)
	localDur := time.Since(tLocal)

	if err != nil {
		e.Log.Warn("local call failed, escalating",
			"err", err, "model", policy.Profile.LocalModel)
		return e.escalate(ctx, bodyWithCloudModel, policy, Report{},
			"local call error: "+err.Error(), localDur)
	}

	localContent, err := extractContent(localResp)
	if err != nil {
		return e.escalate(ctx, bodyWithCloudModel, policy, Report{},
			"malformed local response", localDur)
	}

	// Score
	report := e.score(ctx, prompt, localContent, policy.Profile)

	if !policy.ShouldEscalate() || report.Pass {
		return &Result{
			ResponseBody: localResp,
			ModelUsed:    policy.Profile.LocalModel,
			Escalated:    false,
			Score:        report.Final,
			LocalLatency: localDur,
			SignalReport: report,
		}, nil
	}

	// Escalate
	reasons := failedSignals(report)
	e.Log.Info("escalating",
		"score", report.Final, "threshold", policy.Profile.Threshold,
		"reasons", reasons, "tag", policy.Tag)

	return e.escalate(ctx, bodyWithCloudModel, policy, report,
		"low confidence: "+reasons, localDur)
}

func (e *Escalator) escalate(ctx context.Context, cloudBody []byte,
	policy Policy, localReport Report, reason string,
	localDur time.Duration) (*Result, error) {

	tCloud := time.Now()
	cloudResp, _, err := e.callUpstream(ctx, cloudBody)
	cloudDur := time.Since(tCloud)
	if err != nil {
		return nil, fmt.Errorf("escalation failed: %w", err)
	}
	return &Result{
		ResponseBody: cloudResp,
		ModelUsed:    policy.Profile.CloudModel,
		Escalated:    true,
		Reason:       reason,
		Score:        localReport.Final,
		LocalLatency: localDur,
		CloudLatency: cloudDur,
		SignalReport: localReport,
	}, nil
}

func (e *Escalator) score(ctx context.Context, prompt, response string,
	profile config.EscalationProfile) Report {

	signals := []Signal{
		RefusalSignal(response),
		LengthSignal(response, profile.ExpectedOutputTokens),
		CoherenceSignal(response),
	}
	if profile.EnableCompileCheck {
		if IsCodeRelated(prompt) {
			signals = append(signals, CompileSignal(response))
		}
	}
	if profile.EnableSimilarityCheck && e.Embedder != nil {
		signals = append(signals, SimilaritySignal(ctx, e.Embedder, prompt,
			response, profile.SimilarityFloor))
	}
	return CombineSignals(signals, profile.Threshold)
}

func (e *Escalator) callUpstream(ctx context.Context, body []byte) ([]byte, int, error) {
	return e.Up.Call(ctx, body)
}

func failedSignals(r Report) string {
	var reasons []string
	for _, s := range r.Signals {
		if s.Score < 0.6 && s.Reason != "" {
			reasons = append(reasons, s.Name+"("+s.Reason+")")
		}
	}
	if len(reasons) == 0 {
		return fmt.Sprintf("overall score %.2f", r.Final)
	}
	out := reasons[0]
	for _, r := range reasons[1:] {
		out += ", " + r
	}
	return out
}

// extractContent pulls the first choice's message content from a non-streaming
// OpenAI chat response.
func extractContent(body []byte) (string, error) {
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if len(r.Choices) == 0 {
		return "", errors.New("no choices")
	}
	return r.Choices[0].Message.Content, nil
}

// InjectModel rewrites the "model" field in a raw JSON request body.
// Exposed here so the api layer can prepare both local and cloud variants.
func InjectModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"], _ = json.Marshal(model)
	// Also force stream=false for scoring; we re-stream on the way out if needed.
	m["stream"], _ = json.Marshal(false)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}