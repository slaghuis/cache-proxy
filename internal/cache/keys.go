package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/slaghuis/cache-proxy/internal/api"
)

// Normalize produces a stable string for exact-match hashing and
// a concatenated prompt string for semantic embedding.
func Normalize(req *api.ChatRequest) (exactKey, semanticText string) {
	type canon struct {
		Model    string         `json:"model"`
		Messages []canonMessage `json:"messages"`
		Temp     float32        `json:"temp"`
		MaxTok   int            `json:"max_tokens"`
	}
	type canonMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	c := canon{Model: req.Model}
	if req.Temperature != nil {
		c.Temp = *req.Temperature
	}
	if req.MaxTokens != nil {
		c.MaxTok = *req.MaxTokens
	}

	var promptParts []string
	for _, m := range req.Messages {
		content := extractText(m.Content)
		c.Messages = append(c.Messages, canonMessage{
			Role: m.Role, Content: strings.TrimSpace(content),
		})
		if m.Role != "system" {
			promptParts = append(promptParts, content)
		}
	}

	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	exactKey = hex.EncodeToString(h[:])
	semanticText = strings.Join(promptParts, "\n---\n")
	return
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// string?
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// array of parts?
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
		return b.String()
	}
	return string(raw)
}

// Cacheable decides if a request is safe to cache.
// Rules:
//   - no streaming (we assemble before caching, so we re-serve non-stream)
//     but we DO handle streaming at the response layer.
//   - no tools (tool calls are stateful; agent will likely re-call anyway)
//   - temperature <= 0.3 (determinism)
func Cacheable(req *api.ChatRequest) bool {
	if len(req.Tools) > 0 {
		return false
	}
	if req.Temperature != nil && *req.Temperature > 0.3 {
		return false
	}
	return true
}