# Pinecone RAG Ingestion Implementation Plan

## 1. Scope

Build a production-oriented document ingestion service for a RAG pipeline. The service accepts PDF uploads, stores raw files in MongoDB GridFS, extracts page text, chunks content, generates local Ollama embeddings, upserts vectors into Pinecone, and streams live progress to a polished Tailwind UI through Server-Sent Events.

## 2. Target Structure

```text
pinecone-rag/
├── IMPLEMENTATION_PLAN.md
├── README.md
├── go.mod
├── go.sum
├── main.go
└── static/
    ├── app.js
    └── index.html
```

## 3. Backend Endpoints

- `GET /`
  - Serves the single-page upload dashboard.
- `POST /api/upload`
  - Accepts one multipart PDF file.
  - Stores the raw PDF in MongoDB GridFS.
  - Starts asynchronous ingestion.
  - Returns `{ "job_id": "...", "file_id": "..." }`.
- `GET /api/events/{job_id}`
  - Opens an SSE stream for live ingestion updates.

## 4. Backend Components

- `Config`
  - Loads runtime settings from environment variables.
- `MongoGridFSStore`
  - Owns MongoDB connection and GridFS upload/download behavior.
- `PDFExtractor`
  - Reads a GridFS PDF stream into a temporary file and extracts page text with `github.com/ledongthuc/pdf`.
- `RecursiveChunker`
  - Uses `github.com/tmc/langchaingo/textsplitter`.
  - Chunk size: `1000`.
  - Chunk overlap: `200`.
- `OllamaEmbedder`
  - Calls local Ollama HTTP API.
  - Model: `gemma4:e2b` by default.
- `PineconeIndexer`
  - Uses official Pinecone Go SDK package path `github.com/pinecone-io/go-pinecone/v4/pinecone` in the module.
  - Upserts vectors into the configured Pinecone index.
- `SSEBroker`
  - Tracks per-job subscriber channels.
  - Flushes JSON status events to connected clients.
- `IngestionService`
  - Coordinates upload, extraction, chunking, embedding worker pool, Pinecone batching, error handling, and progress events.

## 5. Ingestion Workflow

1. Validate upload is a PDF.
2. Stream multipart file into MongoDB GridFS.
3. Emit stage 1 completion:
   ```json
   { "stage": 1, "status": "completed", "file_id": "..." }
   ```
4. Read the file back from GridFS.
5. Extract text page-by-page.
6. Emit stage 2 page progress:
   ```json
   { "stage": 2, "status": "processing", "current_page": 14, "total_pages": 42 }
   ```
7. Split each page with recursive character chunking.
8. Send chunk jobs into a bounded worker pool.
9. Workers call Ollama concurrently for embeddings.
10. Batch upsert vectors into Pinecone.
11. Emit stage 3 and stage 4 progress events.
12. Emit final completion event.

## 6. Pinecone Vector Contract

Each vector record must follow this logical contract:

```json
{
  "id": "book_[MONGO_OBJECT_ID]_chunk_[INCREMENTING_INDEX]",
  "values": [0.1, 0.2],
  "metadata": {
    "source_file_id": "[STRING_REPRESENTATION_OF_GRIDFS_OBJECT_ID]",
    "text_content": "[THE_RAW_UNTRUNCATED_1000_CHAR_TEXT_SEGMENT]",
    "chapter": 1,
    "page_number": 42
  }
}
```

## 7. Frontend UI

- Dark minimalist dashboard.
- Drag-and-drop PDF upload card.
- PDF-only validation with immediate error banner.
- Hide drop zone after upload starts.
- Show file card with name, size, and global progress bar.
- Show four-stage conveyor:
  1. Streaming Binary File to Secured Database Storage (GridFS)
  2. Executing Layout Logic & String Extraction
  3. Generating Multi-Dimensional Semantic Embeddings
  4. Upserting Index Arrays to Pinecone Vector Space
- Drive all stage transitions from SSE payloads.
- On backend error:
  - stop loaders
  - turn card border amber/red
  - show verbose error modal
  - expose reset dropzone action

## 8. Runtime Configuration

- `PORT`
- `MONGO_URI`
- `MONGO_DATABASE`
- `GRIDFS_BUCKET`
- `OLLAMA_BASE_URL`
- `OLLAMA_EMBED_MODEL`
- `PINECONE_API_KEY`
- `PINECONE_INDEX`
- `PINECONE_HOST`
- `PINECONE_NAMESPACE`
- `EMBED_WORKERS`
- `UPSERT_BATCH_SIZE`

## 9. Verification

- Run `gofmt`.
- Run `go mod tidy`.
- Run `go test ./...`.
- Manually validate:
  - UI loads.
  - invalid file type is rejected.
  - valid PDF upload returns `job_id` and `file_id`.
  - SSE events stream to the UI.
  - GridFS file can be read back.
  - chunks include source file ID and page metadata.
