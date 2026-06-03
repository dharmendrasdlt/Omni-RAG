package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type App struct {
	cfg      Config
	embedder *OllamaEmbedder
	querier  *PineconeQuerier
	streamer Streamer
}

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

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()

	// Step 1: Embed query
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

	// Step 2: Query Pinecone
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

	// Steps 3 & 4: Build RAG prompt
	system, user := buildRAGPrompt(req.Query, sources)
	log.Printf("[search] prompt built system_len=%d", len(system))

	// Step 5: Stream LLM response
	_ = sseWrite(w, flusher, "stage", stageEvent{Stage: "generating", Message: fmt.Sprintf("Generating answer with %s…", a.streamer.ModelName())})
	log.Printf("[search] step=stream model=%s", a.streamer.ModelName())

	streamCtx, streamCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer streamCancel()

	tokenCount := 0
	err = a.streamer.Stream(streamCtx, system, user, func(token string) error {
		tokenCount++
		return sseWrite(w, flusher, "token", tokenEvent{Text: token})
	})
	if err != nil {
		log.Printf("[search] stream error: %v", err)
		_ = sseWrite(w, flusher, "error", errorEvent{Stage: "generating", Message: fmt.Sprintf("LLM streaming failed: %v", err)})
		return
	}
	log.Printf("[search] stream complete tokens=%d", tokenCount)

	_ = sseWrite(w, flusher, "sources", sourcesEvent{Sources: sources})
	_ = sseWrite(w, flusher, "done", map[string]bool{"ok": true})
}

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
