package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoArticle struct {
	ID      primitive.ObjectID `bson:"_id"`
	Content string             `bson:"content"`
}

type OllamaEmbedder struct{ BaseURL string }

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{"model": "nomic-embed-text", "prompt": text})
	resp, err := http.Post(o.BaseURL+"/api/embeddings", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embedding returned status %d", resp.StatusCode)
	}

	var result struct{ Embedding []float32 }
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding, nil
}

type QdrantClient struct{ BaseURL string }

func (q *QdrantClient) CreateCollection(name string, vectorSize int) error {
	payload := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal qdrant collection payload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPut, q.BaseURL+"/collections/"+name, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach Qdrant during collection creation: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("qdrant collection creation returned status %d: %v", resp.StatusCode, errResp)
	}

	return nil
}

func (q *QdrantClient) CreateTextIndex(collectionName, fieldName string) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"field_name":   fieldName,
		"field_schema": "text",
	})

	req, err := http.NewRequest(http.MethodPut, q.BaseURL+"/collections/"+collectionName+"/index", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create qdrant text index: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("qdrant text index creation returned status %d: %v", resp.StatusCode, errResp)
	}
	return nil
}

func (q *QdrantClient) UpsertDocuments(collectionName string, mongoIDs []string, embeddings [][]float32, documents []string) error {
	points := make([]map[string]interface{}, 0, len(documents))
	for i, doc := range documents {
		points = append(points, map[string]interface{}{
			"id":     i + 1,
			"vector": embeddings[i],
			"payload": map[string]interface{}{
				"mongo_id": mongoIDs[i],
				"content":  doc,
			},
		})
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"points": points,
	})

	url := fmt.Sprintf("%s/collections/%s/points?wait=true", q.BaseURL, collectionName)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant upsert failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("qdrant upsert returned status %d: %v", resp.StatusCode, errResp)
	}
	return nil
}

func main() {
	ctx := context.Background()
	log.Println("Starting Ingestion Service (MongoDB -> Qdrant)...")

	// 1. Connect to MongoDB primary database
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("MongoDB connection failed: %v", err)
	}
	defer mongoClient.Disconnect(ctx)

	mongoColl := mongoClient.Database("content_db").Collection("articles_v2")

	// 2. Read raw content from MongoDB
	cursor, err := mongoColl.Find(ctx, bson.M{})
	if err != nil {
		log.Fatalf("Failed to fetch MongoDB content: %v", err)
	}
	defer cursor.Close(ctx)

	var mongoArticles []MongoArticle
	if err := cursor.All(ctx, &mongoArticles); err != nil {
		log.Fatalf("Failed to decode MongoDB articles: %v", err)
	}

	if len(mongoArticles) == 0 {
		log.Println("No documents found in MongoDB. Please run the Seed Service first!")
		return
	}

	// 3. Connect to Qdrant
	embedder := &OllamaEmbedder{BaseURL: "http://localhost:11434"}
	qdrant := &QdrantClient{BaseURL: "http://localhost:6333"}

	// 4. Ingest documents and their generated embeddings into Qdrant
	var mongoIDs []string
	var embeddings [][]float32
	var documents []string

	for _, art := range mongoArticles {
		fmt.Printf("Generating vector for MongoDB document ID (%s)...\n", art.ID.Hex())
		vector, err := embedder.Embed(ctx, art.Content)
		if err != nil {
			log.Fatalf("Vector generation failed: %v. Make sure Ollama is running and has 'nomic-embed-text' pulled.", err)
		}

		mongoIDs = append(mongoIDs, art.ID.Hex())
		embeddings = append(embeddings, vector)
		documents = append(documents, art.Content)
	}

	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		log.Fatal("No embeddings were generated. Cannot initialize Qdrant collection.")
	}

	const collectionName = "policies_v2"
	if err := qdrant.CreateCollection(collectionName, len(embeddings[0])); err != nil {
		log.Fatalf("Failed to initialize Qdrant collection: %v. Make sure Qdrant is running on port 6333.", err)
	}

	if err := qdrant.CreateTextIndex(collectionName, "content"); err != nil {
		log.Fatalf("Failed to initialize Qdrant content text index: %v", err)
	}

	if err := qdrant.UpsertDocuments(collectionName, mongoIDs, embeddings, documents); err != nil {
		log.Fatalf("Qdrant ingestion failed: %v", err)
	}

	log.Println("Ingestion completed! Stored MongoDB content embeddings successfully inside Qdrant.")
}
