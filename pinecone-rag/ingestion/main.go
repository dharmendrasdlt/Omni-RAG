package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/pinecone-io/go-pinecone/v4/pinecone"
	"github.com/tmc/langchaingo/textsplitter"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	stageUpload  = 1
	stageExtract = 2
	stageEmbed   = 3
	stageUpsert  = 4
)

type Config struct {
	Port            string
	MongoURI        string
	MongoDatabase   string
	GridFSBucket    string
	OllamaBaseURL   string
	OllamaModel     string
	PineconeAPIKey  string
	PineconeIndex   string
	PineconeHost    string
	PineconeNS      string
	EmbedWorkers    int
	UpsertBatchSize int
	MaxUploadBytes  int64
}

// Define a structural mirror matching your JSON configuration file keys
type PineconeJSONConfig struct {
	PineconeAPIKey    string `json:"PINECONE_API_KEY"`
	PineconeIndexName string `json:"PINECONE_INDEX_NAME"`
	PineconeHost      string `json:"PINECONE_HOST"`
	PineconeNamespace string `json:"PINECONE_NAMESPACE"`
	EmbeddingModel    string `json:"EMBEDDING_MODEL"`
}

// LoadPineconeJSON attempts to read and parse the all-caps config file.
// If the file does not exist, it returns an empty configuration struct gracefully.
func LoadPineconeJSON(path string) PineconeJSONConfig {
	var config PineconeJSONConfig

	fileBytes, err := os.ReadFile(path)
	if err != nil {
		// If the file isn't there, we just skip it and rely entirely on env fallbacks
		return config
	}

	if err := json.Unmarshal(fileBytes, &config); err != nil {
		log.Printf("Warning: Found pinecone-config.json but failed to parse it: %v", err)
	}

	return config
}

// LoadConfig initializes your application variables
func loadConfig() Config {
	// 1. Try to load values from the local JSON file first
	jsonCfg := LoadPineconeJSON("../pinecone-config.json")
	log.Println(jsonCfg)
	log.Println(envOrJSON(env("EMBEDDING_MODEL", ""), jsonCfg.EmbeddingModel, "gemma4:e4b"))

	// 2. Build your composite application settings
	return Config{
		Port:          env("PORT", "8080"),
		MongoURI:      env("MONGO_URI", "mongodb://localhost:27017"),
		MongoDatabase: env("MONGO_DATABASE", "rag_documents"),
		GridFSBucket:  env("GRIDFS_BUCKET", "pdf_uploads"),

		// Fallback to json model if present, otherwise read environment
		OllamaBaseURL: strings.TrimRight(env("OLLAMA_BASE_URL", "http://localhost:11434"), "/"),
		OllamaModel:   envOrJSON(env("EMBEDDING_MODEL", ""), jsonCfg.EmbeddingModel, "gemma4:e4b"),

		// Prioritize local JSON variables over terminal exports
		PineconeAPIKey: envOrJSON(os.Getenv("PINECONE_API_KEY"), jsonCfg.PineconeAPIKey, ""),
		PineconeIndex:  envOrJSON(os.Getenv("PINECONE_INDEX"), jsonCfg.PineconeIndexName, ""),
		PineconeHost:   strings.TrimRight(envOrJSON(os.Getenv("PINECONE_HOST"), jsonCfg.PineconeHost, ""), "/"),
		PineconeNS:     envOrJSON(os.Getenv("PINECONE_NAMESPACE"), jsonCfg.PineconeNamespace, ""),

		EmbedWorkers:    envInt("EMBED_WORKERS", 5),
		UpsertBatchSize: envInt("UPSERT_BATCH_SIZE", 50),
		MaxUploadBytes:  int64(envInt("MAX_UPLOAD_MB", 100)) << 20,
	}
}

// Helper utility to choose environment variable first, then fallback to JSON file, then fallback to code defaults.
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
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

type App struct {
	cfg       Config
	store     *MongoGridFSStore
	extractor *PDFExtractor
	chunker   *RecursiveChunker
	embedder  *OllamaEmbedder
	indexer   *PineconeIndexer
	broker    *SSEBroker
	ingest    *IngestionService
}

type SSEEvent struct {
	JobID        string `json:"job_id"`
	Stage        int    `json:"stage"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
	FileID       string `json:"file_id,omitempty"`
	CurrentPage  int    `json:"current_page,omitempty"`
	TotalPages   int    `json:"total_pages,omitempty"`
	CurrentChunk int    `json:"current_chunk,omitempty"`
	TotalChunks  int    `json:"total_chunks,omitempty"`
	Indexed      int    `json:"indexed,omitempty"`
	TotalVectors int    `json:"total_vectors,omitempty"`
	Progress     int    `json:"progress,omitempty"`
	Error        string `json:"error,omitempty"`
}

type SSEBroker struct {
	mu         sync.RWMutex
	subs       map[string]map[chan SSEEvent]struct{}
	history    map[string][]SSEEvent
	maxHistory int
}

func NewSSEBroker(maxHistory int) *SSEBroker {
	return &SSEBroker{
		subs:       make(map[string]map[chan SSEEvent]struct{}),
		history:    make(map[string][]SSEEvent),
		maxHistory: maxHistory,
	}
}

func (b *SSEBroker) Publish(event SSEEvent) {
	b.mu.Lock()
	events := append(b.history[event.JobID], event)
	if len(events) > b.maxHistory {
		events = events[len(events)-b.maxHistory:]
	}
	b.history[event.JobID] = events

	var targets []chan SSEEvent
	for ch := range b.subs[event.JobID] {
		targets = append(targets, ch)
	}
	b.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- event:
		default:
			log.Printf("dropping SSE event for slow subscriber job=%s stage=%d", event.JobID, event.Stage)
		}
	}
}

func (b *SSEBroker) Subscribe(jobID string) (chan SSEEvent, []SSEEvent, func()) {
	ch := make(chan SSEEvent, 32)

	b.mu.Lock()
	if b.subs[jobID] == nil {
		b.subs[jobID] = make(map[chan SSEEvent]struct{})
	}
	b.subs[jobID][ch] = struct{}{}
	history := append([]SSEEvent(nil), b.history[jobID]...)
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if b.subs[jobID] != nil {
			delete(b.subs[jobID], ch)
			if len(b.subs[jobID]) == 0 {
				delete(b.subs, jobID)
			}
		}
		b.mu.Unlock()
		close(ch)
	}

	return ch, history, cancel
}

type MongoGridFSStore struct {
	client *mongo.Client
	bucket *mongo.GridFSBucket
}

func NewMongoGridFSStore(ctx context.Context, cfg Config) (*MongoGridFSStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("connect mongo: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	db := client.Database(cfg.MongoDatabase)
	bucket := db.GridFSBucket(options.GridFSBucket().SetName(cfg.GridFSBucket))
	return &MongoGridFSStore{client: client, bucket: bucket}, nil
}

func (s *MongoGridFSStore) Close(ctx context.Context) error {
	return s.client.Disconnect(ctx)
}

func (s *MongoGridFSStore) UploadPDF(ctx context.Context, filename, contentType string, source io.Reader) (bson.ObjectID, error) {
	fileID := bson.NewObjectID()
	opts := options.GridFSUpload().SetMetadata(bson.D{
		{Key: "content_type", Value: contentType},
		{Key: "uploaded_at", Value: time.Now().UTC()},
	})

	stream, err := s.bucket.OpenUploadStreamWithID(ctx, fileID, filename, opts)
	if err != nil {
		return bson.NilObjectID, fmt.Errorf("open gridfs upload stream: %w", err)
	}

	closed := false
	defer func() {
		if !closed {
			_ = stream.Abort()
		}
	}()

	if _, err := io.Copy(stream, source); err != nil {
		return bson.NilObjectID, fmt.Errorf("write gridfs upload stream: %w", err)
	}
	if err := stream.Close(); err != nil {
		return bson.NilObjectID, fmt.Errorf("close gridfs upload stream: %w", err)
	}
	closed = true
	return fileID, nil
}

func (s *MongoGridFSStore) DownloadToTempFile(ctx context.Context, fileID bson.ObjectID) (string, func(), error) {
	stream, err := s.bucket.OpenDownloadStream(ctx, fileID)
	if err != nil {
		return "", nil, fmt.Errorf("open gridfs download stream: %w", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			log.Printf("close gridfs download stream: %v", err)
		}
	}()

	tmp, err := os.CreateTemp("", "omnirag-gridfs-*.pdf")
	if err != nil {
		return "", nil, fmt.Errorf("create temp pdf: %w", err)
	}

	cleanup := func() {
		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("remove temp pdf %s: %v", tmp.Name(), err)
		}
	}

	if _, err := io.Copy(tmp, stream); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("copy gridfs pdf to temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close temp pdf: %w", err)
	}
	return tmp.Name(), cleanup, nil
}

type PDFExtractor struct{}

type PageText struct {
	PageNumber int
	TotalPages int
	Text       string
}

func (e *PDFExtractor) ExtractPages(path string, onPage func(current, total int)) ([]PageText, error) {
	f, reader, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pdf parser: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("close pdf parser file: %v", err)
		}
	}()

	total := reader.NumPage()
	pages := make([]PageText, 0, total)
	for pageIndex := 1; pageIndex <= total; pageIndex++ {
		page := reader.Page(pageIndex)
		if page.V.IsNull() || page.V.Key("Contents").Kind() == pdf.Null {
			onPage(pageIndex, total)
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return nil, fmt.Errorf("extract page %d: %w", pageIndex, err)
		}
		pages = append(pages, PageText{
			PageNumber: pageIndex,
			TotalPages: total,
			Text:       strings.TrimSpace(text),
		})
		onPage(pageIndex, total)
	}
	return pages, nil
}

type RecursiveChunker struct {
	splitter textsplitter.RecursiveCharacter
}

func NewRecursiveChunker() *RecursiveChunker {
	return &RecursiveChunker{
		splitter: textsplitter.NewRecursiveCharacter(
			textsplitter.WithChunkSize(1000),
			textsplitter.WithChunkOverlap(200),
		),
	}
}

func (c *RecursiveChunker) Split(page PageText) ([]ChunkJob, error) {
	if strings.TrimSpace(page.Text) == "" {
		return nil, nil
	}
	chunks, err := c.splitter.SplitText(page.Text)
	if err != nil {
		return nil, err
	}
	jobs := make([]ChunkJob, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		jobs = append(jobs, ChunkJob{
			PageNumber: page.PageNumber,
			Chapter:    1,
			Text:       chunk,
		})
	}
	return jobs, nil
}

type OllamaEmbedder struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type ollamaEmbedResponse struct {
	Embedding  []float32   `json:"embedding"`
	Embeddings [][]float32 `json:"embeddings"`
}

func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return nil, errors.New("cannot embed empty text")
	}

	reqBody, err := json.Marshal(map[string]any{
		"model": o.Model,
		"input": []string{clean},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close ollama response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama embed status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
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

type PineconeIndexer struct {
	APIKey    string
	IndexName string
	Host      string
	Namespace string
	Client    *http.Client
}

func NewPineconeIndexer(ctx context.Context, cfg Config) (*PineconeIndexer, error) {
	indexer := &PineconeIndexer{
		APIKey:    cfg.PineconeAPIKey,
		IndexName: cfg.PineconeIndex,
		Host:      cfg.PineconeHost,
		Namespace: cfg.PineconeNS,
		Client:    &http.Client{Timeout: 60 * time.Second},
	}
	if indexer.APIKey == "" || indexer.IndexName == "" {
		return indexer, nil
	}
	if indexer.Host != "" {
		return indexer, nil
	}

	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey:    indexer.APIKey,
		SourceTag: "omnirag-pinecone-rag",
	})
	if err != nil {
		return nil, fmt.Errorf("create pinecone sdk client: %w", err)
	}

	idx, err := pc.DescribeIndex(ctx, indexer.IndexName)
	if err != nil {
		return nil, fmt.Errorf("describe pinecone index %q: %w", indexer.IndexName, err)
	}
	indexer.Host = strings.TrimRight(idx.Host, "/")
	return indexer, nil
}

func (p *PineconeIndexer) Upsert(ctx context.Context, vectors []PineconeVector) error {
	if len(vectors) == 0 {
		return nil
	}
	if p.APIKey == "" {
		return errors.New("PINECONE_API_KEY is required")
	}
	if p.IndexName == "" {
		return errors.New("PINECONE_INDEX is required")
	}
	if p.Host == "" {
		return errors.New("PINECONE_HOST is required or index host must be resolvable through Pinecone SDK")
	}

	payload := map[string]any{"vectors": vectors}
	if p.Namespace != "" {
		payload["namespace"] = p.Namespace
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pinecone upsert: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Host+"/vectors/upsert", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", p.APIKey)

	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("pinecone upsert request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close pinecone response body: %v", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pinecone upsert status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type ChunkJob struct {
	Index      int
	PageNumber int
	Chapter    int
	Text       string
}

type EmbeddedChunk struct {
	Job    ChunkJob
	Vector []float32
}

type PineconeVector struct {
	ID       string         `json:"id"`
	Values   []float32      `json:"values"`
	Metadata map[string]any `json:"metadata"`
}

type IngestionService struct {
	store     *MongoGridFSStore
	extractor *PDFExtractor
	chunker   *RecursiveChunker
	embedder  *OllamaEmbedder
	indexer   *PineconeIndexer
	broker    *SSEBroker
	cfg       Config
}

func (s *IngestionService) Start(ctx context.Context, jobID string, fileID bson.ObjectID) {
	go s.run(ctx, jobID, fileID)
}

func (s *IngestionService) run(parent context.Context, jobID string, fileID bson.ObjectID) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.emitError(jobID, 0, fmt.Sprintf("panic recovered: %v", recovered))
		}
	}()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	s.emit(jobID, SSEEvent{Stage: stageExtract, Status: "processing", Message: "Reading PDF back from GridFS", Progress: 18})

	tmpPath, cleanup, err := s.store.DownloadToTempFile(ctx, fileID)
	if err != nil {
		s.emitError(jobID, stageExtract, err.Error())
		return
	}
	defer cleanup()

	pages, err := s.extractor.ExtractPages(tmpPath, func(current, total int) {
		s.emit(jobID, SSEEvent{
			Stage:       stageExtract,
			Status:      "processing",
			Message:     fmt.Sprintf("Reading Page %d of %d...", current, total),
			CurrentPage: current,
			TotalPages:  total,
			Progress:    20 + percent(current, total, 20),
		})
	})
	if err != nil {
		s.emitError(jobID, stageExtract, err.Error())
		return
	}
	s.emit(jobID, SSEEvent{Stage: stageExtract, Status: "completed", Message: "PDF text extraction completed", TotalPages: len(pages), Progress: 40})

	jobs, err := s.buildChunkJobs(pages)
	if err != nil {
		s.emitError(jobID, stageEmbed, err.Error())
		return
	}
	if len(jobs) == 0 {
		s.emitError(jobID, stageEmbed, "no text chunks were generated from the PDF")
		return
	}

	s.emit(jobID, SSEEvent{
		Stage:       stageEmbed,
		Status:      "processing",
		Message:     fmt.Sprintf("Embedding %d chunks with %d workers", len(jobs), s.cfg.EmbedWorkers),
		TotalChunks: len(jobs),
		Progress:    42,
	})

	embedded, err := s.embedChunks(ctx, jobID, jobs)
	if err != nil {
		s.emitError(jobID, stageEmbed, err.Error())
		return
	}
	s.emit(jobID, SSEEvent{Stage: stageEmbed, Status: "completed", Message: "Semantic embeddings generated", TotalChunks: len(embedded), Progress: 72})

	vectors := s.toPineconeVectors(fileID.Hex(), embedded)
	if err := s.upsertVectors(ctx, jobID, vectors); err != nil {
		s.emitError(jobID, stageUpsert, err.Error())
		return
	}

	s.emit(jobID, SSEEvent{
		Stage:        stageUpsert,
		Status:       "completed",
		Message:      "Document indexed into Pinecone memory",
		Indexed:      len(vectors),
		TotalVectors: len(vectors),
		Progress:     100,
	})
	s.emit(jobID, SSEEvent{Stage: 0, Status: "completed", Message: "Ingestion pipeline completed", FileID: fileID.Hex(), Progress: 100})
}

func (s *IngestionService) buildChunkJobs(pages []PageText) ([]ChunkJob, error) {
	var jobs []ChunkJob
	for _, page := range pages {
		chunks, err := s.chunker.Split(page)
		if err != nil {
			return nil, fmt.Errorf("chunk page %d: %w", page.PageNumber, err)
		}
		for _, chunk := range chunks {
			chunk.Index = len(jobs) + 1
			jobs = append(jobs, chunk)
		}
	}
	return jobs, nil
}

func (s *IngestionService) embedChunks(ctx context.Context, jobID string, jobs []ChunkJob) ([]EmbeddedChunk, error) {
	jobCh := make(chan ChunkJob)
	resultCh := make(chan EmbeddedChunk)
	errCh := make(chan error, 1)
	var completed atomic.Int64
	var wg sync.WaitGroup

	workers := s.cfg.EmbedWorkers
	if workers <= 0 {
		workers = 5
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobCh {
				vector, err := s.embedder.Embed(ctx, job.Text)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("worker %d embed chunk %d: %w", workerID, job.Index, err):
					default:
					}
					return
				}
				done := int(completed.Add(1))
				s.emit(jobID, SSEEvent{
					Stage:        stageEmbed,
					Status:       "processing",
					Message:      fmt.Sprintf("Generated vector %d of %d", done, len(jobs)),
					CurrentChunk: done,
					TotalChunks:  len(jobs),
					Progress:     42 + percent(done, len(jobs), 30),
				})
				select {
				case resultCh <- EmbeddedChunk{Job: job, Vector: vector}:
				case <-ctx.Done():
					return
				}
			}
		}(i + 1)
	}

	go func() {
		defer close(jobCh)
		for _, job := range jobs {
			select {
			case jobCh <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make([]EmbeddedChunk, 0, len(jobs))
	for {
		select {
		case err := <-errCh:
			return nil, err
		case result, ok := <-resultCh:
			if !ok {
				sort.Slice(results, func(i, j int) bool {
					return results[i].Job.Index < results[j].Job.Index
				})
				return results, nil
			}
			results = append(results, result)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *IngestionService) toPineconeVectors(fileID string, chunks []EmbeddedChunk) []PineconeVector {
	vectors := make([]PineconeVector, 0, len(chunks))
	for _, chunk := range chunks {
		id := fmt.Sprintf("book_%s_chunk_%d", fileID, chunk.Job.Index)
		vectors = append(vectors, PineconeVector{
			ID:     id,
			Values: chunk.Vector,
			Metadata: map[string]any{
				"source_file_id": fileID,
				"text_content":   chunk.Job.Text,
				"chapter":        chunk.Job.Chapter,
				"page_number":    chunk.Job.PageNumber,
			},
		})
	}
	return vectors
}

func (s *IngestionService) upsertVectors(ctx context.Context, jobID string, vectors []PineconeVector) error {
	batchSize := s.cfg.UpsertBatchSize
	if batchSize <= 0 {
		batchSize = 50
	}

	indexed := 0
	for start := 0; start < len(vectors); start += batchSize {
		end := start + batchSize
		if end > len(vectors) {
			end = len(vectors)
		}
		batch := vectors[start:end]
		s.emit(jobID, SSEEvent{
			Stage:        stageUpsert,
			Status:       "processing",
			Message:      fmt.Sprintf("Upserting vectors %d-%d of %d", start+1, end, len(vectors)),
			Indexed:      indexed,
			TotalVectors: len(vectors),
			Progress:     74 + percent(indexed, len(vectors), 24),
		})
		if err := s.indexer.Upsert(ctx, batch); err != nil {
			return err
		}
		indexed = end
		s.emit(jobID, SSEEvent{
			Stage:        stageUpsert,
			Status:       "processing",
			Message:      fmt.Sprintf("Indexed %d of %d vectors", indexed, len(vectors)),
			Indexed:      indexed,
			TotalVectors: len(vectors),
			Progress:     74 + percent(indexed, len(vectors), 24),
		})
	}
	return nil
}

func (s *IngestionService) emit(jobID string, event SSEEvent) {
	event.JobID = jobID
	s.broker.Publish(event)
}

func (s *IngestionService) emitError(jobID string, stage int, message string) {
	s.emit(jobID, SSEEvent{
		Stage:    stage,
		Status:   "error",
		Message:  message,
		Error:    message,
		Progress: 0,
	})
}

func percent(current, total, span int) int {
	if total <= 0 {
		return 0
	}
	value := int(float64(current) / float64(total) * float64(span))
	if value < 0 {
		return 0
	}
	if value > span {
		return span
	}
	return value
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes)
	if err := r.ParseMultipartForm(a.cfg.MaxUploadBytes); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid multipart upload: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing multipart field 'file'")
		return
	}
	defer closeWithLog("multipart upload stream", file)

	if err := validatePDF(header); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	reader, contentType, err := pdfReaderWithSniff(file)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if contentType != "application/pdf" {
		writeJSONError(w, http.StatusBadRequest, "file content is not a PDF")
		return
	}

	uploadCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	fileID, err := a.store.UploadPDF(uploadCtx, header.Filename, contentType, reader)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobID := bson.NewObjectID().Hex()
	a.broker.Publish(SSEEvent{
		JobID:    jobID,
		Stage:    stageUpload,
		Status:   "completed",
		Message:  "Uploaded and secured document in MongoDB GridFS",
		FileID:   fileID.Hex(),
		Progress: 15,
	})
	a.ingest.Start(context.Background(), jobID, fileID)

	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id":  jobID,
		"file_id": fileID.Hex(),
	})
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if jobID == "" || jobID == r.URL.Path {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, history, unsubscribe := a.broker.Subscribe(jobID)
	defer unsubscribe()

	for _, event := range history {
		if err := writeSSE(w, event); err != nil {
			return
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func validatePDF(header *multipart.FileHeader) error {
	if header == nil {
		return errors.New("missing file header")
	}
	if !strings.EqualFold(filepath.Ext(header.Filename), ".pdf") {
		return errors.New("only .pdf files are allowed")
	}
	return nil
}

func pdfReaderWithSniff(file multipart.File) (io.Reader, string, error) {
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("read file header: %w", err)
	}
	contentType := http.DetectContentType(header[:n])
	if n < 4 || string(header[:4]) != "%PDF" {
		return nil, contentType, errors.New("file signature is not PDF")
	}
	return io.MultiReader(bytes.NewReader(header[:n]), file), "application/pdf", nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeSSE(w io.Writer, event SSEEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
	return err
}

func closeWithLog(name string, closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		log.Printf("close %s: %v", name, err)
	}
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

func main() {
	cfg := loadConfig()
	ctx := context.Background()

	store, err := NewMongoGridFSStore(ctx, cfg)
	if err != nil {
		log.Fatalf("initialize MongoDB GridFS: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Close(shutdownCtx); err != nil {
			log.Printf("disconnect mongo: %v", err)
		}
	}()

	indexer, err := NewPineconeIndexer(ctx, cfg)
	if err != nil {
		log.Printf("warning: Pinecone index host was not resolved at startup: %v", err)
		indexer = &PineconeIndexer{
			APIKey:    cfg.PineconeAPIKey,
			IndexName: cfg.PineconeIndex,
			Host:      cfg.PineconeHost,
			Namespace: cfg.PineconeNS,
			Client:    &http.Client{Timeout: 60 * time.Second},
		}
	}

	app := &App{
		cfg:       cfg,
		store:     store,
		extractor: &PDFExtractor{},
		chunker:   NewRecursiveChunker(),
		embedder: &OllamaEmbedder{
			BaseURL: cfg.OllamaBaseURL,
			Model:   cfg.OllamaModel,
			Client:  &http.Client{Timeout: 90 * time.Second},
		},
		indexer: indexer,
		broker:  NewSSEBroker(256),
	}
	app.ingest = &IngestionService{
		store:     app.store,
		extractor: app.extractor,
		chunker:   app.chunker,
		embedder:  app.embedder,
		indexer:   app.indexer,
		broker:    app.broker,
		cfg:       cfg,
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./static")))
	mux.HandleFunc("/api/upload", app.handleUpload)
	mux.HandleFunc("/api/events/", app.handleEvents)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           recoverMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Pinecone RAG ingestion dashboard listening on http://localhost:%s", cfg.Port)
	log.Fatal(server.ListenAndServe())
}
