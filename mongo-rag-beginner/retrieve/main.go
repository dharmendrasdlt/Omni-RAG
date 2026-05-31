package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Document struct {
	Content   string    `bson:"content"`
	Embedding []float32 `bson:"embedding"`
}

type Generator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// --- Ollama Implementation (Local) ---
type OllamaClient struct {
	BaseURL string
}

func (o *OllamaClient) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  "nomic-embed-text",
		"prompt": text,
	})
	resp, err := http.Post(o.BaseURL+"/api/embeddings", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama API returned status %d", resp.StatusCode)
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

func (o *OllamaClient) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  "gemma4:e2b",
		"prompt": prompt,
		"stream": false,
	})
	resp, err := http.Post(o.BaseURL+"/api/generate", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API returned status %d", resp.StatusCode)
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Response, nil
}

// --- Claude Implementation (Anthropic) ---
type ClaudeClient struct {
	APIKey string
}

func (c *ClaudeClient) Generate(ctx context.Context, prompt string) (string, error) {
	url := "https://api.anthropic.com/v1/messages"
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return "", fmt.Errorf("claude api error: %v", errResp)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "No response.", nil
	}
	return result.Content[0].Text, nil
}

// --- Server API Handler ---

type QueryRequest struct {
	Query  string `json:"query"`
	Format string `json:"format"` // "prose" | "table" | "json"
}

type QueryResponse struct {
	Answer    string  `json:"answer"`
	Source    string  `json:"source"`
	Score     float32 `json:"score"`
	Generator string  `json:"generator"`
}

func handleQuery(coll *mongo.Collection, embedder *OllamaClient, generator Generator, genName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Enable CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// A. Embed query
		vector, err := embedder.Embed(ctx, req.Query)
		if err != nil {
			http.Error(w, "Failed to generate embedding: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// B. Retrieve best match from MongoDB
		cursor, err := coll.Find(ctx, bson.M{})
		if err != nil {
			http.Error(w, "Database query failed", http.StatusInternalServerError)
			return
		}
		defer cursor.Close(ctx)

		var bestContent string
		var bestScore float32 = -1.0
		for cursor.Next(ctx) {
			var doc Document
			cursor.Decode(&doc)
			var score float32
			for i := 0; i < len(vector) && i < len(doc.Embedding); i++ {
				score += vector[i] * doc.Embedding[i]
			}
			if score > bestScore {
				bestScore = score
				bestContent = doc.Content
			}
		}

		if bestContent == "" {
			http.Error(w, "No knowledge base documents found. Ingest data first.", http.StatusNotFound)
			return
		}

		// C. Formulate Prompt based on requested format
		formatInstruction := "Answer the query in clear, concise paragraphs."
		if req.Format == "table" {
			formatInstruction = "Structure your answer strictly as a clean Markdown Table mapping key points or criteria."
		} else if req.Format == "json" {
			formatInstruction = "Structure your response as a valid JSON object matching the query schema."
		}

		prompt := fmt.Sprintf(`You are a precise corporate assistant. Answer the query based ONLY on the provided context. If you cannot answer it, say "I cannot answer this based on the provided information."

Format Requirement: %s

Context:
%s

Query:
%s

Answer:`, formatInstruction, bestContent, req.Query)

		// D. Generate answer
		answer, err := generator.Generate(ctx, prompt)
		if err != nil {
			http.Error(w, "Generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(QueryResponse{
			Answer:    answer,
			Source:    bestContent,
			Score:     bestScore,
			Generator: genName,
		})
	}
}

func main() {
	ctx := context.Background()
	ollama := &OllamaClient{BaseURL: "http://localhost:11434"}

	var generator Generator
	genName := "Claude 3.5 Sonnet"
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		generator = &ClaudeClient{APIKey: key}
	} else {
		generator = ollama
		genName = "Ollama (gemma4:e2b)"
	}

	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("Mongo error: %v", err)
	}
	defer client.Disconnect(ctx)

	coll := client.Database("rag_demo").Collection("documents")

	// Static Web UI Files
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	// Query API Endpoint
	http.HandleFunc("/api/query", handleQuery(coll, ollama, generator, genName))

	fmt.Println("Retrieval Service + Search UI listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
