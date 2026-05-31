package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type Generator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// --- Claude & Ollama Clients ---
type OllamaClient struct{ BaseURL string }

type OllamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

func (o *OllamaClient) Embed(ctx context.Context, text string) ([]float32, error) {
	cleanText := strings.TrimSpace(text)
	if cleanText == "" {
		return nil, fmt.Errorf("cannot embed an empty string")
	}

	payloadText := "search_query: " + cleanText

	reqBody, err := json.Marshal(map[string]interface{}{
		"model": "nomic-embed-text",
		"input": []string{payloadText},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %v", err)
	}

	resp, err := http.Post(o.BaseURL+"/api/embed", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("network error hitting ollama: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama server returned error status code: %d", resp.StatusCode)
	}

	var res OllamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	if len(res.Embeddings) == 0 || len(res.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("ollama returned no matrices. Ensure model is loaded")
	}

	return res.Embeddings[0], nil
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
		return "", fmt.Errorf("ollama generation returned status %d", resp.StatusCode)
	}

	var res struct{ Response string }
	json.NewDecoder(resp.Body).Decode(&res)
	return res.Response, nil
}

type ClaudeClient struct{ APIKey string }

func (c *ClaudeClient) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 1024,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(reqBody))
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

	var res struct{ Content []struct{ Text string } }
	json.NewDecoder(resp.Body).Decode(&res)
	if len(res.Content) == 0 {
		return "No response.", nil
	}
	return res.Content[0].Text, nil
}

// --- ChromaDB Retrieval Client ---
type ChromaClient struct {
	BaseURL string
}

type ChromaQueryResult struct {
	Documents [][]string  `json:"documents"`
	Distances [][]float32 `json:"distances"`
}

// Fixed to natively filter documents within Chroma using keyword constraints
func (c *ChromaClient) QueryCollection(collName string, queryVector []float32, rawQuery string) (string, float32, error) {
	resp, err := http.Get(c.BaseURL + "/api/v2/tenants/default/databases/default/collections/" + collName)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("collection %q not found in ChromaDB via API v2 routes", collName)
	}
	var coll struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&coll)

	// Build payload payload mapping 2 candidate results maximum
	reqPayload := map[string]interface{}{
		"query_embeddings": [][]float32{queryVector},
		"n_results":        2,
	}

	// Apply Chroma's internal metadata string condition for isolated short keyword inputs
	if !strings.Contains(strings.TrimSpace(rawQuery), " ") {
		reqPayload["where_document"] = map[string]interface{}{
			"$contains": rawQuery,
		}
	}

	reqBody, _ := json.Marshal(reqPayload)
	queryURL := fmt.Sprintf("%s/api/v2/tenants/default/databases/default/collections/%s/query", c.BaseURL, coll.ID)
	qResp, err := http.Post(queryURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", 0, err
	}
	defer qResp.Body.Close()

	var result ChromaQueryResult
	if err := json.NewDecoder(qResp.Body).Decode(&result); err != nil {
		return "", 0, err
	}

	// FALLBACK SAFETY: If string filter yields 0 matches, run again immediately via raw vector similarity math
	if len(result.Documents) == 0 || len(result.Documents[0]) == 0 {
		delete(reqPayload, "where_document")
		reqBody, _ = json.Marshal(reqPayload)
		qRespRetry, retryErr := http.Post(queryURL, "application/json", bytes.NewBuffer(reqBody))
		if retryErr == nil {
			defer qRespRetry.Body.Close()
			var retryResult ChromaQueryResult
			if decodeErr := json.NewDecoder(qRespRetry.Body).Decode(&retryResult); decodeErr == nil {
				result = retryResult
			}
		}
	}

	if len(result.Documents) == 0 || len(result.Documents[0]) == 0 {
		return "", 0, fmt.Errorf("no match found inside collection index")
	}

	// Merge matches cleanly together inside a string builder block
	var contextBuilder strings.Builder
	var highestSimilarity float32 = 0.0

	for i := 0; i < len(result.Documents[0]); i++ {
		docText := result.Documents[0][i]
		contextBuilder.WriteString(fmt.Sprintf("- %s\n", docText))

		var distance float32 = 0.0
		if len(result.Distances) > 0 && len(result.Distances[0]) > i {
			distance = result.Distances[0][i]
		}

		similarity := 1.0 - (distance / 2.0)
		if similarity < 0 {
			similarity = 0
		}
		if i == 0 {
			highestSimilarity = similarity
		}
	}

	return contextBuilder.String(), highestSimilarity, nil
}

// --- HTTP API Controller ---

type QueryRequest struct {
	Query  string `json:"query"`
	Format string `json:"format"`
}

type QueryResponse struct {
	Answer    string  `json:"answer"`
	Source    string  `json:"source"`
	Score     float32 `json:"score"`
	Generator string  `json:"generator"`
}

func handleQuery(chroma *ChromaClient, embedder *OllamaClient, generator Generator, genName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { // <--- The Fix
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

		if r.Method == http.MethodOptions {
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

		// A. Generate Query Vector via Ollama
		queryVec, err := embedder.Embed(ctx, req.Query)
		if err != nil {
			http.Error(w, "Embedding query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// B. Query ChromaDB with our self-contained keyword fallback setup
		combinedContent, score, err := chroma.QueryCollection("policies_v2", queryVec, req.Query)
		if err != nil {
			http.Error(w, "ChromaDB query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("--- DEBUG: Chroma Match Score: %f ---", score)
		log.Printf("--- DEBUG: Retried Context Content: \n%s\n", combinedContent)

		// C. Formulate Prompt based on requested format
		formatInstruction := "Answer the query in clear, concise paragraphs."
		switch req.Format {
		case "table":
			formatInstruction = "Structure your answer strictly as a clean Markdown Table mapping key points or criteria."
		case "json":
			formatInstruction = "Structure your response as a valid JSON object matching the query schema."
		}

		prompt := fmt.Sprintf(`You are a precise corporate assistant. Answer the query based ONLY on the provided context retrieved from our database. If you cannot answer it, say "I cannot answer this based on the provided information."

Format Requirement: %s

Context:
%s

Query:
%s

Answer:`, formatInstruction, combinedContent, req.Query)

		// D. Generate answer
		answer, err := generator.Generate(ctx, prompt)
		if err != nil {
			http.Error(w, "Generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(QueryResponse{
			Answer:    answer,
			Source:    combinedContent,
			Score:     score,
			Generator: genName,
		})
	}
}

func main() {
	ollama := &OllamaClient{BaseURL: "http://localhost:11434"}
	chroma := &ChromaClient{BaseURL: "http://localhost:8000"}

	var generator Generator
	genName := "Claude 3.5 Sonnet"
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		generator = &ClaudeClient{APIKey: key}
	} else {
		generator = ollama
		genName = "Ollama (Gemma 4 e2b)"
	}

	// Serve Static UI Files
	http.Handle("/", http.FileServer(http.Dir("./static")))

	// Serve query API with standard 4 signatures matching untouched architecture requirements
	http.HandleFunc("/api/query", handleQuery(chroma, ollama, generator, genName))

	fmt.Println("Retrieval Web Server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
