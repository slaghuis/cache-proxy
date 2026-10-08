package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Ollama struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		BaseURL: baseURL, Model: model,
		HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.Model, "input": text})
	req, err := http.NewRequestWithContext(ctx, "POST",
		o.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ollama status %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("empty")
	}
	return out.Embeddings[0], nil
}