package api

import "encoding/json"

type ChatRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Temperature *float32        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	User        string          `json:"user,omitempty"`

	// pass-through — we only inspect what matters
	Raw map[string]json.RawMessage `json:"-"`
}

type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // can be string or array
	Name    string          `json:"name,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// Metadata we add
	Cached     string `json:"x_cache,omitempty"` // "exact" | "semantic" | ""
	CacheScore float32 `json:"x_cache_score,omitempty"`
}

type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type ResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}