package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"
)

// ─── Configuration ────────────────────────────────────────────────────────────

type Config struct {
	Port            string
	OllamaBaseURL   string
	OllamaModel     string
	GenerationModel string
	PineconeAPIKey  string
	PineconeHost    string
	PineconeIndex   string
	PineconeNS      string
	TopK            int
}

// Define the exact structural mirror matching your ALL_CAPS JSON keys
type PineconeJSONConfig struct {
	PineconeAPIKey    string `json:"PINECONE_API_KEY"`
	PineconeIndexName string `json:"PINECONE_INDEX_NAME"`
	PineconeHost      string `json:"PINECONE_HOST"`
	PineconeNamespace string `json:"PINECONE_NAMESPACE"`
	EmbeddingModel    string `json:"EMBEDDING_MODEL"`
	GenerationModel   string `json:"GENERATION_MODEL"`
}

// LoadPineconeJSON attempts to read and parse the all-caps config file.
func LoadPineconeJSON(path string) PineconeJSONConfig {
	var config PineconeJSONConfig

	fileBytes, err := os.ReadFile(path)
	if err != nil {
		// If file doesn't exist, skip it and rely on environment variables
		return config
	}

	if err := json.Unmarshal(fileBytes, &config); err != nil {
		log.Printf("Warning: Found pinecone-config.json but failed to parse it: %v", err)
	}

	return config
}

// loadConfig initializes your retrieval service settings
func loadConfig() Config {
	// 1. Load the values from the local JSON file
	jsonCfg := LoadPineconeJSON("../pinecone-config.json")

	// 2. Return your updated composite configurations
	return Config{
		Port:          env("PORT", "8081"),
		OllamaBaseURL: strings.TrimRight(env("OLLAMA_BASE_URL", "http://localhost:11434"), "/"),

		// Falls back to JSON configuration value if environment variable is missing
		OllamaModel:     envOrJSON(env("EMBEDDING_MODEL", ""), jsonCfg.EmbeddingModel, "gemma4:e2b"),
		GenerationModel: envOrJSON(env("GENERATION_MODEL", ""), jsonCfg.GenerationModel, "gemma4:e4b"),

		// Prioritizes terminal environment exports first, then file configurations
		PineconeAPIKey: envOrJSON(os.Getenv("PINECONE_API_KEY"), jsonCfg.PineconeAPIKey, ""),
		PineconeHost:   strings.TrimRight(envOrJSON(os.Getenv("PINECONE_HOST"), jsonCfg.PineconeHost, ""), "/"),
		PineconeIndex:  envOrJSON(os.Getenv("PINECONE_INDEX"), jsonCfg.PineconeIndexName, ""),
		PineconeNS:     envOrJSON(os.Getenv("PINECONE_NAMESPACE"), jsonCfg.PineconeNamespace, ""),

		TopK: envInt("RETRIEVAL_TOP_K", 3),
	}
}

// Helper utility to cascade string resolution across Env -> JSON File -> Defaults
func envOrJSON(envVal string, jsonVal string, defaultVal string) string {
	if envVal != "" {
		return envVal
	}
	if jsonVal != "" {
		return jsonVal
	}
	return defaultVal
}

func env(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// ─── Domain Types ─────────────────────────────────────────────────────────────

type SearchRequest struct {
	Query string `json:"query"`
}

// SourceMatch represents a single retrieved vector chunk and its metadata.
type SourceMatch struct {
	ID           string  `json:"id"`
	Score        float64 `json:"score"`
	SourceFileID string  `json:"source_file_id"`
	TextContent  string  `json:"text_content"`
	Chapter      int     `json:"chapter"`
	PageNumber   int     `json:"page_number"`
}

// ─── SSE Event Payloads ───────────────────────────────────────────────────────

type stageEvent struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type tokenEvent struct {
	Text string `json:"text"`
}

type sourcesEvent struct {
	Sources []SourceMatch `json:"sources"`
}

type errorEvent struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// ─── Step 1 — Ollama Embedder ─────────────────────────────────────────────────

type OllamaEmbedder struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type ollamaEmbedResponse struct {
	Embedding  []float32   `json:"embedding"`
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed converts a text query into a float32 vector via Ollama /api/embed.
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

// ─── Step 2 — Pinecone Querier ────────────────────────────────────────────────

// PineconeQuerier resolves the index host (via SDK if needed) and
// issues raw REST queries for maximum compatibility.
type PineconeQuerier struct {
	APIKey    string
	Host      string // resolved data-plane host
	Namespace string
	TopK      int
	Client    *http.Client
}

// NewPineconeQuerier builds the querier and resolves the index host when
// PINECONE_HOST is not explicitly set, using the official Go SDK.
func NewPineconeQuerier(ctx context.Context, cfg Config) (*PineconeQuerier, error) {
	q := &PineconeQuerier{
		APIKey:    cfg.PineconeAPIKey,
		Host:      cfg.PineconeHost,
		Namespace: cfg.PineconeNS,
		TopK:      cfg.TopK,
		Client:    &http.Client{Timeout: 30 * time.Second},
	}

	// If host already provided, skip SDK host discovery.
	if q.Host != "" {
		return q, nil
	}

	if q.APIKey == "" || cfg.PineconeIndex == "" {
		// Missing creds — callers will receive an error on first Query call.
		return q, nil
	}

	// Use official Pinecone SDK only for host discovery (DescribeIndex).
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

// Query sends the embedding vector to Pinecone and returns the top-K source matches.
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

// ─── Steps 3 & 4 — Prompt Builder ────────────────────────────────────────────

// buildPrompt constructs the strict RAG system prompt from retrieved chunks.
func buildPrompt(query string, sources []SourceMatch) string {
	var sb strings.Builder

	sb.WriteString("[System Instruction]: You are a technical assistant. ")
	sb.WriteString("Answer the user's question using ONLY the context block provided below. ")
	sb.WriteString("If the answer cannot be found in the context, state clearly that you do not know. ")
	sb.WriteString("Do not make up facts.\n\n")
	sb.WriteString("[Context Content]:\n")

	for i, s := range sources {
		sb.WriteString(fmt.Sprintf("--- Source %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("Document ID: %s | Chapter: %d | Page: %d\n", s.SourceFileID, s.Chapter, s.PageNumber))
		sb.WriteString(fmt.Sprintf("Content: %q\n", s.TextContent))
		sb.WriteString("----------------------\n")
	}

	sb.WriteString(fmt.Sprintf("\n[User Question]: %s\n", query))
	return sb.String()
}

// ─── Step 5 — Ollama Streamer ─────────────────────────────────────────────────

type OllamaStreamer struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateChunk struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Stream sends the RAG prompt to Ollama /api/generate and calls onToken for
// every streamed token. Returns nil when the model finishes.
func (o *OllamaStreamer) Stream(ctx context.Context, prompt string, onToken func(string) error) error {
	body, err := json.Marshal(ollamaGenerateRequest{
		Model:  o.Model,
		Prompt: prompt,
		Stream: true,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama generate request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close ollama generate response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ollama generate status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase scanner buffer for large context responses.
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaGenerateChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			log.Printf("parse ollama chunk: %v (line: %q)", err, line)
			continue
		}
		if chunk.Response != "" {
			if err := onToken(chunk.Response); err != nil {
				return err
			}
		}
		if chunk.Done {
			break
		}
	}
	return scanner.Err()
}

// ─── App & HTTP Layer ─────────────────────────────────────────────────────────

type App struct {
	cfg      Config
	embedder *OllamaEmbedder
	querier  *PineconeQuerier
	streamer *OllamaStreamer
}

// sseWrite writes a single SSE event and flushes immediately.
func sseWrite(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// handleSearch is the main RAG pipeline endpoint.
// It returns an SSE stream containing: stage → token(s) → sources → done | error.
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body.
	var req SearchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeJSONError(w, http.StatusBadRequest, "query must not be empty")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported by this server")
		return
	}

	// Switch to SSE mode.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()

	// ── Step 1: Embed Query ──────────────────────────────────────────────────
	_ = sseWrite(w, flusher, "stage", stageEvent{Stage: "embedding", Message: "Generating query embedding via Ollama…"})
	log.Printf("[search] query=%q step=embed", req.Query)

	embedCtx, embedCancel := context.WithTimeout(ctx, 45*time.Second)
	defer embedCancel()

	vector, err := a.embedder.Embed(embedCtx, req.Query)
	if err != nil {
		log.Printf("[search] embed error: %v", err)
		_ = sseWrite(w, flusher, "error", errorEvent{Stage: "embedding", Message: fmt.Sprintf("Embedding failed: %v", err)})
		return
	}
	log.Printf("[search] embed ok dim=%d", len(vector))

	// ── Step 2: Query Pinecone ───────────────────────────────────────────────
	_ = sseWrite(w, flusher, "stage", stageEvent{Stage: "retrieval", Message: fmt.Sprintf("Querying Pinecone for top %d matches…", a.cfg.TopK)})
	log.Printf("[search] step=pinecone-query top_k=%d", a.cfg.TopK)

	queryCtx, queryCancel := context.WithTimeout(ctx, 20*time.Second)
	defer queryCancel()

	sources, err := a.querier.Query(queryCtx, vector)
	if err != nil {
		log.Printf("[search] pinecone error: %v", err)
		_ = sseWrite(w, flusher, "error", errorEvent{Stage: "retrieval", Message: fmt.Sprintf("Pinecone query failed: %v", err)})
		return
	}
	if len(sources) == 0 {
		_ = sseWrite(w, flusher, "error", errorEvent{Stage: "retrieval", Message: "No matching documents found in the knowledge base for this query."})
		return
	}
	log.Printf("[search] pinecone ok matches=%d", len(sources))

	// ── Steps 3 & 4: Build strict RAG prompt ─────────────────────────────────
	prompt := buildPrompt(req.Query, sources)
	log.Printf("[search] prompt built len=%d", len(prompt))

	// ── Step 5: Stream LLM response ──────────────────────────────────────────
	_ = sseWrite(w, flusher, "stage", stageEvent{Stage: "generating", Message: "Generating answer with Gemma 4…"})
	log.Printf("[search] step=ollama-stream model=%s", a.cfg.OllamaModel)

	streamCtx, streamCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer streamCancel()

	tokenCount := 0
	err = a.streamer.Stream(streamCtx, prompt, func(token string) error {
		tokenCount++
		return sseWrite(w, flusher, "token", tokenEvent{Text: token})
	})
	if err != nil {
		log.Printf("[search] stream error: %v", err)
		_ = sseWrite(w, flusher, "error", errorEvent{Stage: "generating", Message: fmt.Sprintf("LLM streaming failed: %v", err)})
		return
	}
	log.Printf("[search] stream complete tokens=%d", tokenCount)

	// ── Send citations & close ────────────────────────────────────────────────
	_ = sseWrite(w, flusher, "sources", sourcesEvent{Sources: sources})
	_ = sseWrite(w, flusher, "done", map[string]bool{"ok": true})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic recovered path=%s err=%v", r.URL.Path, recovered)
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Entry Point ──────────────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	ctx := context.Background()

	// Resolve Pinecone host at startup (uses SDK DescribeIndex when PINECONE_HOST is unset).
	querier, err := NewPineconeQuerier(ctx, cfg)
	if err != nil {
		log.Printf("warning: Pinecone host could not be resolved at startup: %v — continuing; runtime queries will fail", err)
		querier = &PineconeQuerier{
			APIKey:    cfg.PineconeAPIKey,
			Host:      cfg.PineconeHost,
			Namespace: cfg.PineconeNS,
			TopK:      cfg.TopK,
			Client:    &http.Client{Timeout: 30 * time.Second},
		}
	}

	app := &App{
		cfg: cfg,
		embedder: &OllamaEmbedder{
			BaseURL: cfg.OllamaBaseURL,
			Model:   cfg.OllamaModel,
			Client:  &http.Client{Timeout: 60 * time.Second},
		},
		querier: querier,
		streamer: &OllamaStreamer{
			BaseURL: cfg.OllamaBaseURL,
			Model:   cfg.GenerationModel,
			Client:  &http.Client{Timeout: 5 * time.Minute},
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./static")))
	mux.HandleFunc("/api/search", app.handleSearch)

	handler := corsMiddleware(recoverMiddleware(mux))

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("OmniRAG Retrieval Service →  http://localhost:%s", cfg.Port)
	log.Printf("Embedding model : %s @ %s", cfg.OllamaModel, cfg.OllamaBaseURL)
	log.Printf("Generation model : %s @ %s", cfg.GenerationModel, cfg.OllamaBaseURL)
	log.Printf("Pinecone host: %s (top_k=%d)", querier.Host, cfg.TopK)
	log.Fatal(server.ListenAndServe())
}
