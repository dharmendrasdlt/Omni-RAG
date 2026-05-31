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

type ChromaClient struct{ BaseURL string }

// Init ensures the fundamental storage infrastructure exists on application load
func (c *ChromaClient) Init() error {
	dbPayload, _ := json.Marshal(map[string]interface{}{"name": "default"})
	dbUrl := c.BaseURL + "/api/v2/tenants/default/databases"

	resp, err := http.Post(dbUrl, "application/json", bytes.NewBuffer(dbPayload))
	if err != nil {
		return fmt.Errorf("failed to reach ChromaDB during initialization: %v", err)
	}
	defer resp.Body.Close()

	// 201 Created means success. 409 Conflict means it already exists, which is perfectly fine.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("chroma database initialization failed with status %d: %v", resp.StatusCode, errResp)
	}

	return nil
}

// GetOrCreateCollection can now focus purely on its single responsibility
func (c *ChromaClient) GetOrCreateCollection(name string) (string, error) {
	payload := map[string]interface{}{
		"name":          name,
		"get_or_create": true,
		"metadata": map[string]interface{}{
			"hnsw:space": "cosine",
		},
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal collection payload: %v", err)
	}

	collUrl := c.BaseURL + "/api/v2/tenants/default/databases/default/collections"
	resp, err := http.Post(collUrl, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return "", fmt.Errorf("chroma collection creation returned status %d: %v", resp.StatusCode, errResp)
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("failed to decode chroma response: %v", err)
	}

	return res.ID, nil
}

// 🛠️ FIXED: Pointed the ingestion payload array straight to the updated default collection path
func (c *ChromaClient) AddDocuments(collID string, ids []string, embeddings [][]float32, documents []string) error {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"ids":        ids,
		"embeddings": embeddings,
		"documents":  documents,
	})

	url := fmt.Sprintf("%s/api/v2/tenants/default/databases/default/collections/%s/add", c.BaseURL, collID)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("chroma add returned status %d: %v", resp.StatusCode, errResp)
	}
	return nil
}

func main() {
	ctx := context.Background()
	log.Println("Starting Ingestion Service (MongoDB -> ChromaDB)...")

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

	// 3. Connect to ChromaDB
	embedder := &OllamaEmbedder{BaseURL: "http://localhost:11434"}
	chroma := &ChromaClient{BaseURL: "http://localhost:8000"}

	// Run structural initialization right here on load
	log.Println("Initializing structural database contexts...")
	if err := chroma.Init(); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}

	collID, err := chroma.GetOrCreateCollection("policies_v2")
	if err != nil {
		log.Fatalf("Failed to connect to ChromaDB: %v. Make sure Chroma is running on port 8000.", err)
	}

	// 4. Ingest documents and their generated embeddings into ChromaDB
	var ids []string
	var embeddings [][]float32
	var documents []string

	for _, art := range mongoArticles {
		fmt.Printf("Generating vector for MongoDB document ID (%s)...\n", art.ID.Hex())
		vector, err := embedder.Embed(ctx, art.Content)
		if err != nil {
			log.Fatalf("Vector generation failed: %v. Make sure Ollama is running and has 'nomic-embed-text' pulled.", err)
		}

		ids = append(ids, art.ID.Hex())
		embeddings = append(embeddings, vector)
		documents = append(documents, art.Content)
	}

	err = chroma.AddDocuments(collID, ids, embeddings, documents)
	if err != nil {
		log.Fatalf("ChromaDB ingestion failed: %v", err)
	}

	log.Println("Ingestion completed! Stored MongoDB content embeddings successfully inside ChromaDB.")
}
