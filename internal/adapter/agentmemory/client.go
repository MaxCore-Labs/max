package agentmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

const searchTimeout = 2 * time.Second

// HTTPClient implements app.EpisodicMemory over loopback REST with a 2s search timeout and 5-error circuit breaker.
type HTTPClient struct {
	baseURL string
	secret  string
	client  *http.Client
	breaker *Breaker
}

// NewHTTPClient constructs a circuit-broken HTTP EpisodicMemory client.
func NewHTTPClient(baseURL, secret string, clock app.Clock) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		client:  &http.Client{Timeout: 5 * time.Second},
		breaker: NewBreaker(clock),
	}
}

// Healthy delegates to the circuit breaker state.
func (c *HTTPClient) Healthy() bool {
	return c.breaker.Healthy()
}

// Search queries /agentmemory/smart-search scoped to agentID with a hard 2-second timeout.
func (c *HTTPClient) Search(ctx context.Context, agentID domain.ID, query string, limit int) ([]app.MemoryHit, error) {
	if !c.breaker.Allow() {
		return nil, ErrCircuitOpen
	}
	if limit <= 0 {
		limit = 5
	}
	sctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	payload, _ := json.Marshal(map[string]any{
		"agentId": agentID,
		"query":   query,
		"limit":   limit,
	})
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, c.baseURL+"/agentmemory/smart-search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.breaker.RecordFailure()
		return nil, fmt.Errorf("agentmemory search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		c.breaker.RecordFailure()
		return nil, fmt.Errorf("agentmemory search HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Results []struct {
			ID             string  `json:"id"`
			AgentID        string  `json:"agentId"`
			ConversationID string  `json:"conversationId"`
			Content        string  `json:"content"`
			Score          float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		c.breaker.RecordFailure()
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	c.breaker.RecordSuccess()

	out := make([]app.MemoryHit, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		out = append(out, app.MemoryHit{
			ID:             domain.ID(r.ID),
			AuthorAgentID:  domain.ID(r.AgentID),
			ConversationID: domain.ID(r.ConversationID),
			Content:        r.Content,
			Score:          r.Score,
		})
	}
	return out, nil
}

// Remember posts one redacted summary to /agentmemory/remember.
func (c *HTTPClient) Remember(ctx context.Context, agentID domain.ID, content string, concepts []string) error {
	if !c.breaker.Allow() {
		return ErrCircuitOpen
	}
	clean := strings.TrimSpace(platform.Redact(content))
	if clean == "" {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"agentId":  agentID,
		"content":  clean,
		"concepts": concepts,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agentmemory/remember", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.breaker.RecordFailure()
		return fmt.Errorf("agentmemory remember: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.breaker.RecordFailure()
		return fmt.Errorf("agentmemory remember HTTP %d", resp.StatusCode)
	}
	c.breaker.RecordSuccess()
	return nil
}
