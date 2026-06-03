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
	"time"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"
)

type PineconeQuerier struct {
	APIKey    string
	Host      string
	Namespace string
	TopK      int
	Client    *http.Client
}

func NewPineconeQuerier(ctx context.Context, cfg Config) (*PineconeQuerier, error) {
	q := &PineconeQuerier{
		APIKey:    cfg.PineconeAPIKey,
		Host:      cfg.PineconeHost,
		Namespace: cfg.PineconeNS,
		TopK:      cfg.TopK,
		Client:    &http.Client{Timeout: 30 * time.Second},
	}

	if q.Host != "" {
		return q, nil
	}

	if q.APIKey == "" || cfg.PineconeIndex == "" {
		return q, nil
	}

	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey:    q.APIKey,
		SourceTag: "omnirag-retrieval",
	})
	if err != nil {
		return nil, fmt.Errorf("create pinecone sdk client: %w", err)
	}

	idx, err := pc.DescribeIndex(ctx, cfg.PineconeIndex)
	if err != nil {
		return nil, fmt.Errorf("describe pinecone index %q: %w", cfg.PineconeIndex, err)
	}
	q.Host = strings.TrimRight(idx.Host, "/")
	log.Printf("Pinecone index host resolved: %s", q.Host)
	return q, nil
}

type pineconeQueryRequest struct {
	Vector          []float32 `json:"vector"`
	TopK            int       `json:"topK"`
	IncludeMetadata bool      `json:"includeMetadata"`
	Namespace       string    `json:"namespace,omitempty"`
}

type pineconeMatch struct {
	ID       string                 `json:"id"`
	Score    float64                `json:"score"`
	Metadata map[string]interface{} `json:"metadata"`
}

type pineconeQueryResponse struct {
	Matches []pineconeMatch `json:"matches"`
}

func (p *PineconeQuerier) Query(ctx context.Context, vector []float32) ([]SourceMatch, error) {
	if p.APIKey == "" {
		return nil, errors.New("PINECONE_API_KEY is not configured")
	}
	if p.Host == "" {
		return nil, errors.New("PINECONE_HOST is not configured and could not be resolved via SDK")
	}

	reqBody := pineconeQueryRequest{
		Vector:          vector,
		TopK:            p.TopK,
		IncludeMetadata: true,
	}
	if p.Namespace != "" {
		reqBody.Namespace = p.Namespace
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal pinecone query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Host+"/query", bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", p.APIKey)

	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pinecone query request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close pinecone query response body: %v", closeErr)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("pinecone query status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded pineconeQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode pinecone query response: %w", err)
	}

	sources := make([]SourceMatch, 0, len(decoded.Matches))
	for _, m := range decoded.Matches {
		src := SourceMatch{ID: m.ID, Score: m.Score}
		if meta := m.Metadata; meta != nil {
			if v, ok := meta["source_file_id"].(string); ok {
				src.SourceFileID = v
			}
			if v, ok := meta["text_content"].(string); ok {
				src.TextContent = v
			}
			if v, ok := meta["chapter"].(float64); ok {
				src.Chapter = int(v)
			}
			if v, ok := meta["page_number"].(float64); ok {
				src.PageNumber = int(v)
			}
		}
		sources = append(sources, src)
	}
	return sources, nil
}
