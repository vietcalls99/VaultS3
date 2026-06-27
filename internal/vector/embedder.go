package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Embedder turns text into embedding vectors.
type Embedder interface {
	// Embed returns one vector per input string, in order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// OpenAICompatEmbedder calls any OpenAI-compatible /v1/embeddings endpoint.
// This keeps VaultS3 model-agnostic: it works with OpenAI, Ollama, llama.cpp,
// LM Studio, vLLM, and other servers that speak the same API — so users pick
// their own (often local, private) embedding model.
type OpenAICompatEmbedder struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
}

// NewOpenAICompatEmbedder creates an embedder. endpoint is the full embeddings
// URL (e.g. http://localhost:11434/v1/embeddings); apiKey may be empty for local
// servers; timeoutSecs<=0 defaults to 30s.
func NewOpenAICompatEmbedder(endpoint, apiKey, model string, timeoutSecs int) *OpenAICompatEmbedder {
	if timeoutSecs <= 0 {
		timeoutSecs = 30
	}
	return &OpenAICompatEmbedder{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		client:   &http.Client{Timeout: time.Duration(timeoutSecs) * time.Second},
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed implements Embedder against the OpenAI embeddings API shape.
func (e *OpenAICompatEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // cap at 64MB
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding endpoint returned %d: %s", resp.StatusCode, string(raw))
	}

	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if er.Error != nil {
		return nil, fmt.Errorf("embedding endpoint error: %s", er.Error.Message)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: got %d vectors for %d inputs", len(er.Data), len(texts))
	}

	// Honor the API's "index" field so order is correct regardless of response order.
	out := make([][]float32, len(texts))
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embedding response index %d out of range", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if len(v) == 0 {
			return nil, fmt.Errorf("embedding endpoint returned no vector for input %d", i)
		}
	}
	return out, nil
}
