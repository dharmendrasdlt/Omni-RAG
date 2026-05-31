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
		"model":  "gemma4:e4b",
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

// --- Qdrant Retrieval Client ---
type QdrantClient struct {
	BaseURL string
}

type QdrantSearchResult struct {
	Result []struct {
		Score   float32                `json:"score"`
		Payload map[string]interface{} `json:"payload"`
	} `json:"result"`
}

func (q *QdrantClient) QueryCollection(collectionName string, queryVector []float32, rawQuery string) (string, float32, error) {
	resp, err := http.Get(q.BaseURL + "/collections/" + collectionName)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("collection %q not found in Qdrant", collectionName)
	}

	reqPayload := map[string]interface{}{
		"vector":       queryVector,
		"limit":        2,
		"with_payload": true,
	}

	useKeywordFilter := !strings.Contains(strings.TrimSpace(rawQuery), " ")
	if useKeywordFilter {
		reqPayload["filter"] = map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "content",
					"match": map[string]interface{}{
						"text": rawQuery,
					},
				},
			},
		}
	}

	result, err := q.search(collectionName, reqPayload)
	if err != nil {
		return "", 0, err
	}

	if len(result.Result) == 0 && useKeywordFilter {
		delete(reqPayload, "filter")
		retryResult, retryErr := q.search(collectionName, reqPayload)
		if retryErr == nil {
			result = retryResult
		}
	}

	if len(result.Result) == 0 {
		return "", 0, fmt.Errorf("no match found inside collection index")
	}

	// Merge matches cleanly together inside a string builder block
	var contextBuilder strings.Builder
	var highestSimilarity float32 = 0.0

	for i, point := range result.Result {
		docText, _ := point.Payload["content"].(string)
		if strings.TrimSpace(docText) == "" {
			continue
		}
		contextBuilder.WriteString(fmt.Sprintf("- %s\n", docText))

		if i == 0 {
			highestSimilarity = point.Score
		}
	}

	if contextBuilder.Len() == 0 {
		return "", 0, fmt.Errorf("qdrant matches did not include content payloads")
	}

	return contextBuilder.String(), highestSimilarity, nil
}

func (q *QdrantClient) search(collectionName string, payload map[string]interface{}) (QdrantSearchResult, error) {
	reqBody, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/collections/%s/points/search", q.BaseURL, collectionName)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return QdrantSearchResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return QdrantSearchResult{}, fmt.Errorf("qdrant search returned status %d: %v", resp.StatusCode, errResp)
	}

	var result QdrantSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return QdrantSearchResult{}, err
	}
	return result, nil
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

func handleQuery(qdrant *QdrantClient, embedder *OllamaClient, generator Generator, genName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		// B. Query Qdrant with keyword filtering and semantic fallback
		combinedContent, score, err := qdrant.QueryCollection("policies_v2", queryVec, req.Query)
		if err != nil {
			http.Error(w, "Qdrant query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("--- DEBUG: Qdrant Match Score: %f ---", score)
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
	qdrant := &QdrantClient{BaseURL: "http://localhost:6333"}

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

	http.HandleFunc("/api/query", handleQuery(qdrant, ollama, generator, genName))

	fmt.Println("Retrieval Web Server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
