package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAICompatEmbedder drives the embedder against a stub that mimics the
// OpenAI /v1/embeddings response shape, including out-of-order data + index.
func TestOpenAICompatEmbedder(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model

		// Respond with vectors in REVERSE order to verify index handling.
		resp := embedResponse{}
		for i := len(req.Input) - 1; i >= 0; i-- {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{float32(i), 1, 0}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(srv.URL, "secret-key", "test-model", 5)
	out, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d vectors, want 3", len(out))
	}
	// out[i] must correspond to input i despite reversed response order.
	for i := range out {
		if out[i][0] != float32(i) {
			t.Fatalf("vector %d mismatched (reorder by index failed): %v", i, out[i])
		}
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
	if gotModel != "test-model" {
		t.Fatalf("model = %q, want test-model", gotModel)
	}
}

func TestEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(srv.URL, "", "x", 5)
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

func TestEmbedderCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return only one vector for two inputs.
		resp := embedResponse{}
		resp.Data = append(resp.Data, struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Embedding: []float32{1, 2}, Index: 0})
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(srv.URL, "", "x", 5)
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected count-mismatch error")
	}
}

func TestEmbedderEmptyInput(t *testing.T) {
	e := NewOpenAICompatEmbedder("http://unused", "", "x", 5)
	out, err := e.Embed(context.Background(), nil)
	if err != nil || out != nil {
		t.Fatalf("empty input should return (nil,nil), got (%v,%v)", out, err)
	}
}
