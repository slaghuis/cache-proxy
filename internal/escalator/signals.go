package escalator

import (
	"context"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/slaghuis/cache-proxy/internal/config"
	"github.com/slaghuis/cache-proxy/internal/embedder"
)

type Signal struct {
	Name   string
	Score  float64 // 0..1
	Weight float64
	Reason string
}

type Report struct {
	Final   float64  `json:"final_score"`
	Pass    bool     `json:"pass"`
	Signals []Signal `json:"signals"`
}

// --- Signal: refusal markers ---

var refusalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bi (?:cannot|can'?t|am unable to)\b`),
	regexp.MustCompile(`(?i)\bas an ai\b`),
	regexp.MustCompile(`(?i)\bi (?:do not|don'?t) (?:know|have (?:access|information))\b`),
	regexp.MustCompile(`(?i)\bi'?m not (?:sure|certain|able)\b.{0,40}\bhelp\b`),
	regexp.MustCompile(`(?i)\bwithout (?:more )?(?:context|information)\b.{0,40}\bcannot\b`),
}

func RefusalSignal(response string) Signal {
	s := Signal{Name: "refusal", Weight: 2.0, Score: 1.0}
	for _, re := range refusalPatterns {
		if re.MatchString(response) {
			s.Score = 0.0
			s.Reason = "matched refusal pattern: " + re.String()
			return s
		}
	}
	return s
}

// --- Signal: length ratio ---

func LengthSignal(response string, expectedTokens int) Signal {
	s := Signal{Name: "length", Weight: 1.0}
	tokens := approxTokens(response)
	if expectedTokens <= 0 {
		s.Score = 1.0
		return s
	}
	ratio := float64(tokens) / float64(expectedTokens)
	switch {
	case ratio < 0.1:
		s.Score = 0.1
		s.Reason = "response far too short"
	case ratio < 0.3:
		s.Score = 0.5
		s.Reason = "response short"
	case ratio > 4.0:
		s.Score = 0.6
		s.Reason = "response unusually long"
	default:
		// Peak at ratio=1.0; drop off smoothly either side.
		s.Score = math.Exp(-math.Pow(math.Log(ratio), 2) / 1.5)
	}
	return s
}

func approxTokens(s string) int {
	return utf8.RuneCountInString(s) / 4
}

// --- Signal: structural coherence ---

func CoherenceSignal(response string) Signal {
	s := Signal{Name: "coherence", Weight: 1.0, Score: 1.0}

	// Count code fence pairs
	fenceCount := strings.Count(response, "```")
	if fenceCount%2 != 0 {
		s.Score = 0.3
		s.Reason = "unclosed code fence"
		return s
	}

	// Abrupt truncation markers
	trimmed := strings.TrimSpace(response)
	if len(trimmed) == 0 {
		s.Score = 0.0
		s.Reason = "empty response"
		return s
	}
	last := trimmed[len(trimmed)-1]
	// Response ends mid-sentence (no terminal punctuation, no closing brace, no newline-after-fence)
	if !strings.HasSuffix(trimmed, "```") &&
		!strings.ContainsRune(".!?})]>", rune(last)) {
		// Only penalize if the response is substantial — short replies may legitimately
		// end without punctuation.
		if len(trimmed) > 200 {
			s.Score = 0.6
			s.Reason = "possible truncation (no terminal punctuation)"
		}
	}

	// Unbalanced braces across whole response
	open := strings.Count(response, "{") - strings.Count(response, "}")
	if open > 2 || open < -2 {
		s.Score = math.Min(s.Score, 0.5)
		s.Reason = "unbalanced braces"
	}
	return s
}

// --- Signal: prompt-answer similarity ---

func SimilaritySignal(ctx context.Context, emb *embedder.Ollama,
	prompt, response string, floor float64) Signal {

	s := Signal{Name: "similarity", Weight: 1.5, Score: 1.0}
	if emb == nil || len(response) < 50 {
		return s // skip for tiny responses
	}

	pv, err1 := emb.Embed(ctx, prompt)
	rv, err2 := emb.Embed(ctx, response)
	if err1 != nil || err2 != nil {
		return s // graceful skip
	}
	sim := cosine(pv, rv)
	if sim < floor {
		s.Score = 0.2
		s.Reason = "low prompt-answer similarity"
		return s
	}
	// Scale: floor=0.0, 0.7+ = 1.0
	s.Score = math.Min(1.0, (sim-floor)/(0.7-floor))
	return s
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// --- Combination ---

func CombineSignals(signals []Signal, threshold float64) Report {
	var sum, weight float64
	for _, s := range signals {
		sum += s.Score * s.Weight
		weight += s.Weight
	}
	final := 0.0
	if weight > 0 {
		final = sum / weight
	}
	return Report{
		Final:   final,
		Pass:    final >= threshold,
		Signals: signals,
	}
}

// IsCodeRelated checks if a prompt looks like it wants code output.
func IsCodeRelated(prompt string) bool {
	lp := strings.ToLower(prompt)
	keywords := []string{
		"golang", "go code", "function", "implement", "refactor",
		"fix the", "write a", "method", "struct", "interface",
		"test for", "benchmark", "handler", "middleware",
	}
	for _, k := range keywords {
		if strings.Contains(lp, k) {
			return true
		}
	}
	// Any code fence in prompt → likely wants code back
	return strings.Contains(prompt, "```")
}

// Config knobs
var _ = config.EscalationProfile{}