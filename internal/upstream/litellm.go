package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL, APIKey: apiKey,
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

// Call makes a non-streaming chat completion.
func (c *Client) Call(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// Stream calls upstream in SSE mode and invokes onChunk per data: line.
// Returns the fully assembled plain-text content for caching.
func (c *Client) Stream(ctx context.Context, body []byte, onChunk func([]byte) error) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("upstream %d: %s", resp.StatusCode, string(b))
	}

	var full strings.Builder
	var model string
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)

	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				idx := bytes.Index(buf, []byte("\n"))
				if idx == -1 {
					break
				}
				line := buf[:idx]
				buf = buf[idx+1:]
				if err := onChunk(append(append([]byte{}, line...), '\n')); err != nil {
					return "", "", err
				}
				content, m := parseSSELine(line)
				if content != "" {
					full.WriteString(content)
				}
				if m != "" {
					model = m
				}
			}
		}
		if err == io.EOF {
			// flush trailing
			if len(buf) > 0 {
				_ = onChunk(buf)
			}
			break
		}
		if err != nil {
			return "", "", err
		}
	}
	return full.String(), model, nil
}

func parseSSELine(line []byte) (content, model string) {
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return "", ""
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		return "", ""
	}
	var chunk struct {
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", ""
	}
	if len(chunk.Choices) > 0 {
		return chunk.Choices[0].Delta.Content, chunk.Model
	}
	return "", chunk.Model
}