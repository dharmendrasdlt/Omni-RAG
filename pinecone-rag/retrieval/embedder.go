package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type OllamaEmbedder struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type ollamaEmbedResponse struct {
	Embedding  []float32   `json:"embedding"`
	Embeddings [][]float32 `json:"embeddings"`
}

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("cannot embed empty text")
	}

	body, err := json.Marshal(map[string]any{
		"model": o.Model,
		"input": []string{text},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close ollama embed response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama embed status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode ollama embed response: %w", err)
	}

	if len(decoded.Embeddings) > 0 && len(decoded.Embeddings[0]) > 0 {
		return decoded.Embeddings[0], nil
	}
	if len(decoded.Embedding) > 0 {
		return decoded.Embedding, nil
	}
	return nil, errors.New("ollama returned no embedding vectors")
}
