package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

func main() {
	cfg := loadConfig()
	ctx := context.Background()

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

	var streamer Streamer
	if cfg.AnthropicAPIKey != "" && cfg.AnthropicModel != "" && cfg.AnthropicHasCredits {
		streamer = &AnthropicStreamer{
			APIKey: cfg.AnthropicAPIKey,
			Model:  cfg.AnthropicModel,
			Client: &http.Client{Timeout: 5 * time.Minute},
		}
		log.Printf("Generation backend : Anthropic (%s)", cfg.AnthropicModel)
	} else {
		streamer = &OllamaStreamer{
			BaseURL: cfg.OllamaBaseURL,
			Model:   cfg.GenerationModel,
			Client:  &http.Client{Timeout: 5 * time.Minute},
		}
		log.Printf("Generation backend : Ollama (%s @ %s)", cfg.GenerationModel, cfg.OllamaBaseURL)
	}

	app := &App{
		cfg: cfg,
		embedder: &OllamaEmbedder{
			BaseURL: cfg.OllamaBaseURL,
			Model:   cfg.OllamaModel,
			Client:  &http.Client{Timeout: 60 * time.Second},
		},
		querier:  querier,
		streamer: streamer,
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
	log.Printf("Embedding model  : %s @ %s", cfg.OllamaModel, cfg.OllamaBaseURL)
	log.Printf("Pinecone host    : %s (top_k=%d)", querier.Host, cfg.TopK)
	log.Fatal(server.ListenAndServe())
}
