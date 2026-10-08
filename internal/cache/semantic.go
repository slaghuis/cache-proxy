package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"

	"github.com/slaghuis/cache-proxy/internal/api"
)

type Semantic struct {
	client     *qdrant.Client
	collection string
	threshold  float32
	ttlHours   int
}

func NewSemantic(host string, port int, collection string, threshold float32, ttlHours int) (*Semantic, error) {
	c, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: port})
	if err != nil {
		return nil, err
	}
	return &Semantic{client: c, collection: collection, threshold: threshold, ttlHours: ttlHours}, nil
}

type cacheEntry struct {
	Response  *api.ChatResponse `json:"response"`
	Model     string            `json:"model"`
	CreatedAt int64             `json:"created_at"`
}

func (s *Semantic) Lookup(ctx context.Context, vec []float32, model string) (*api.ChatResponse, float32, bool) {
	limit := uint64(1)
	resp, err := s.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collection,
		Query:          qdrant.NewQuery(vec...),
		Limit:          &limit,
		WithPayload:    qdrant.NewWithPayload(true),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("model", model),
			},
		},
	})
	if err != nil || len(resp) == 0 {
		return nil, 0, false
	}
	hit := resp[0]
	if hit.Score < s.threshold {
		return nil, hit.Score, false
	}
	pl := hit.Payload
	rawJSON := pl["entry"].GetStringValue()
	var entry cacheEntry
	if err := json.Unmarshal([]byte(rawJSON), &entry); err != nil {
		return nil, hit.Score, false
	}
	// TTL check
	if time.Since(time.Unix(entry.CreatedAt, 0)) > time.Duration(s.ttlHours)*time.Hour {
		_, _ = s.client.Delete(ctx, &qdrant.DeletePoints{
			CollectionName: s.collection,
			Points:         qdrant.NewPointsSelector(hit.Id),
		})
		return nil, hit.Score, false
	}
	return entry.Response, hit.Score, true
}

func (s *Semantic) Store(ctx context.Context, vec []float32, model string, resp *api.ChatResponse) error {
	entry := cacheEntry{Response: resp, Model: model, CreatedAt: time.Now().Unix()}
	b, _ := json.Marshal(entry)

	payload := qdrant.NewValueMap(map[string]any{
		"model": model,
		"entry": string(b),
	})

	id := uuid.New().String()
	_, err := s.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: s.collection,
		Points: []*qdrant.PointStruct{{
			Id:      qdrant.NewIDUUID(id),
			Vectors: qdrant.NewVectors(vec...),
			Payload: payload,
		}},
	})
	if err != nil {
		return fmt.Errorf("qdrant upsert: %w", err)
	}
	return nil
}