# Pinecone RAG Ingestion Service

This service implements a full-stack PDF ingestion pipeline for OmniRAG. It stores raw PDF files in MongoDB GridFS, extracts text page-by-page, chunks text with LangChainGo, generates local Ollama embeddings, upserts vectors into Pinecone, and streams live progress to a Tailwind-powered dashboard through Server-Sent Events.

## Architecture

```text
Browser Upload UI
    │
    │ POST /api/upload multipart PDF
    ▼
Go Upload Handler
    │
    ├── validates .pdf extension and PDF signature
    ├── streams raw binary into MongoDB GridFS
    └── starts async ingestion job
          │
          ├── reads PDF back from GridFS by ObjectID
          ├── extracts page text with github.com/ledongthuc/pdf
          ├── chunks each page with RecursiveCharacter splitter
          ├── embeds chunks concurrently through Ollama /api/embed
          └── upserts vectors into Pinecone /vectors/upsert

GET /api/events/{job_id} streams live SSE progress to the UI.
```

## Data Traceability

MongoDB GridFS is the raw source of truth. Every vector written to Pinecone includes the GridFS ObjectID in both its vector ID and metadata:

```json
{
  "id": "book_[MONGO_OBJECT_ID]_chunk_[INCREMENTING_INDEX]",
  "values": [0.1, 0.2],
  "metadata": {
    "source_file_id": "[MONGO_OBJECT_ID]",
    "text_content": "[RAW 1000 CHAR CHUNK]",
    "chapter": 1,
    "page_number": 42
  }
}
```

That means a retrieval result from Pinecone can always be traced back to the original PDF binary stored in GridFS.

## Endpoints

### `GET /`

Serves the upload dashboard.

### `POST /api/upload`

Accepts a multipart PDF upload. The form field must be named `file`.

Response:

```json
{
  "job_id": "665a...",
  "file_id": "665b..."
}
```

### `GET /api/events/{job_id}`

Streams Server-Sent Events. Event payloads are JSON objects:

```json
{
  "job_id": "...",
  "stage": 2,
  "status": "processing",
  "message": "Reading Page 14 of 42...",
  "current_page": 14,
  "total_pages": 42,
  "progress": 31
}
```

## Runtime Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP server port. |
| `MONGO_URI` | `mongodb://localhost:27017` | MongoDB connection string. |
| `MONGO_DATABASE` | `rag_documents` | Database for GridFS bucket. |
| `GRIDFS_BUCKET` | `pdf_uploads` | GridFS bucket prefix. |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Local Ollama server. |
| `OLLAMA_EMBED_MODEL` | `gemma4:e2b` | Ollama model used for embeddings. |
| `PINECONE_API_KEY` | unset | Pinecone API key. Required for upsert. |
| `PINECONE_INDEX` | unset | Pinecone index name. Required for host discovery. |
| `PINECONE_HOST` | unset | Pinecone index host. Optional if `PINECONE_INDEX` can be described through SDK. |
| `PINECONE_NAMESPACE` | unset | Optional Pinecone namespace. |
| `EMBED_WORKERS` | `5` | Concurrent embedding worker count. |
| `UPSERT_BATCH_SIZE` | `50` | Pinecone upsert batch size. |
| `MAX_UPLOAD_MB` | `100` | Upload size limit. |

## Prerequisites

Start MongoDB:

```bash
docker run -d -p 27017:27017 --name mongodb mongo:latest
```

Start Ollama and pull the configured model:

```bash
ollama pull gemma4:e2b
```

Set Pinecone configuration:

```bash
export PINECONE_API_KEY="your-key"
export PINECONE_INDEX="your-index"
# Optional if SDK DescribeIndex can resolve the host:
export PINECONE_HOST="https://your-index-host"
```

## Run

```bash
cd /Users/dharmendra/golang-projects/Omni-RAG/pinecone-rag
go mod tidy
go run .
```

Open:

```text
http://localhost:8080
```

## Verification

```bash
gofmt -w main.go
go test ./...
```

## Notes

- The PDF parser is native Go and does not perform OCR.
- Page numbers are preserved from the PDF extraction loop.
- Chapter defaults to `1` because chapter detection is not reliably available from plain PDF text extraction.
- Multipart file streams, GridFS streams, PDF parser files, temporary files, and HTTP response bodies are closed with explicit cleanup paths.
