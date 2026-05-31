package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Document struct {
	Content   string    `bson:"content"`
	Embedding []float32 `bson:"embedding"`
}

type OllamaEmbedder struct {
	BaseURL string
}

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
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

func main() {
	ctx := context.Background()
	log.Println("Starting Ingestion Service...")

	// 1. Connect to MongoDB
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer mongoClient.Disconnect(ctx)

	db := mongoClient.Database("rag_demo")
	collection := db.Collection("documents")

	// Clean older database entries
	collection.Drop(ctx)

	// 2. Sample data to ingest
	passages := []string{
		"Standard working hours are 9 AM to 5 PM, Monday through Friday.",
		"Employees can work remotely up to 2 days per week with manager approval.",
		"Health insurance benefits are fully covered for all full-time employees starting their first day.",
		"Expense reimbursement requests must be submitted within 30 days of the purchase.",
	}

	embedder := &OllamaEmbedder{BaseURL: "http://localhost:11434"}

	// 3. Ingest documents
	for i, text := range passages {
		fmt.Printf("[%d/%d] Generating embedding and saving document...\n", i+1, len(passages))
		vector, err := embedder.Embed(ctx, text)
		if err != nil {
			log.Fatalf("Embedding failed: %v. Is Ollama running with 'nomic-embed-text' pulled?", err)
		}

		doc := Document{
			Content:   text,
			Embedding: vector,
		}
		_, err = collection.InsertOne(ctx, doc)
		if err != nil {
			log.Fatalf("Failed to insert document to MongoDB: %v", err)
		}
	}

	log.Println("Ingestion completed successfully!")
}
