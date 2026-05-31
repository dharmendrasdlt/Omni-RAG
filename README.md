# OmniRAG 🚀

**OmniRAG** is a high-performance, enterprise-grade Retrieval-Augmented Generation (RAG) engine written in Go. 

Unlike standard out-of-the-box RAG setups that fall apart on real-world data, OmniRAG is built to work *on top of existing data* without forcing database schema migrations, intrusive backend re-indexing, or requiring end-users to master complex prompt engineering. It handles localized short keyword searches and abstract semantic queries with equal precision by shifting retrieval intelligence entirely to the orchestration layer.

---

## 🏗️ Core Architecture

OmniRAG isolates the database layer from the AI retrieval logic. It treats your primary database (MongoDB in the current implementation) as the source of truth, while using document-aware routing and conditional fallback mechanisms inside the vector layer (ChromaDB) to improve deterministic accuracy.

The current implementation lives in `chromadb-rag/`. It seeds MongoDB with source policy content, ingests that content into ChromaDB with Ollama-generated embeddings, and serves a retrieval UI/API that applies keyword-sensitive Chroma `where_document` filtering before sending the final context to Claude 3.5 Sonnet or local Ollama Gemma 4 e2b.

```text
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│                                        OmniRAG DFD                                           │
│          Go orchestration over MongoDB source data, ChromaDB retrieval, and LLM generation   │
└──────────────────────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│  1. Ingestion / Input Layer                                                                  │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                              │
│  Source data path                                                                            │
│                                                                                              │
│  ┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐    │
│  │ chromadb-rag/seed/main.go    │        │ MongoDB Source of Truth                      │    │
│  │                              │        │                                              │    │
│  │ - Connects via MONGO_URI     │───────▶│ Database: content_db                         │    │
│  │ - Drops demo collection      │        │ Collection: articles_v2                      │    │
│  │ - Inserts policy documents   │        │ Document field: content                      │    │
│  └──────────────────────────────┘        └──────────────────┬───────────────────────────┘    │
│                                                             │                                │
│                                                             │ Find all source articles        │
│                                                             ▼                                │
│  ┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐    │
│  │ chromadb-rag/ingest/main.go  │        │ Ollama Embedding Service                     │    │
│  │                              │        │ http://localhost:11434                       │    │
│  │ - Reads MongoDB articles     │───────▶│ POST /api/embeddings                         │    │
│  │ - Embeds article content     │        │ model: nomic-embed-text                      │    │
│  │ - Creates Chroma database    │◀───────│ payload: { "prompt": article.content }       │    │
│  │ - Creates policies_v2 index  │        └──────────────────────────────────────────────┘    │
│  └──────────────┬───────────────┘                                                            │
│                 │                                                                            │
│                 │ Add ids, documents, embeddings                                             │
│                 ▼                                                                            │
│  ┌──────────────────────────────────────────────────────────────────────────────────────┐    │
│  │ ChromaDB Vector Store                                                                │    │
│  │ http://localhost:8000                                                                │    │
│  │                                                                                      │    │
│  │ API v2 tenant/database: default/default                                              │    │
│  │ Collection: policies_v2                                                              │    │
│  │ Collection metadata: { "hnsw:space": "cosine" }                                      │    │
│  └──────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                              │
│  Runtime query path                                                                           │
│                                                                                              │
│  ┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐    │
│  │ Browser Search UI            │        │ Direct API Client                            │    │
│  │ chromadb-rag/retrieve/static │        │ curl, service, script, or integration        │    │
│  └──────────────┬───────────────┘        └──────────────────┬───────────────────────────┘    │
│                 │                                           │                                │
│                 └───────────────────────┬───────────────────┘                                │
│                                         ▼                                                    │
│                         POST /api/query                                                       │
│                         { "query": "...", "format": "prose|table|json" }                     │
└─────────────────────────────────────────┬────────────────────────────────────────────────────┘
                                          │
                                          ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│  2. Orchestration / Router Layer                                                             │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────────────────────┐    │
│  │ chromadb-rag/retrieve/main.go                                                       │    │
│  │ HTTP server: http://localhost:8080                                                   │    │
│  │                                                                                      │    │
│  │ Routes implemented:                                                                  │    │
│  │ - GET  /                         static web UI                                       │    │
│  │ - POST /api/query                retrieval + generation endpoint                     │    │
│  │ - OPTIONS /api/query             CORS preflight                                      │    │
│  │                                                                                      │    │
│  │ Current query orchestration:                                                         │    │
│  │ - Parse JSON request                                                                 │    │
│  │ - Embed query with Ollama /api/embed                                                 │    │
│  │ - Prefix query text as "search_query: ..."                                           │    │
│  │ - Query ChromaDB collection policies_v2                                              │    │
│  │ - Apply where_document only for single-token short queries                           │    │
│  │ - Retry without where_document when filtered search returns zero documents            │    │
│  │ - Merge up to 2 Chroma results into one context block                                │    │
│  │ - Build a grounded prompt with the requested output format                           │    │
│  │ - Route generation to Claude or Ollama                                               │    │
│  └──────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                              │
│  Query routing behavior                                                                       │
│                                                                                              │
│  ┌──────────────────────────────────────────────┐      ┌─────────────────────────────────┐   │
│  │ Short Keyword Query                          │      │ Complex Phrase Query             │   │
│  │ Example: "health"                            │      │ Example: "working remotely"      │   │
│  │                                              │      │                                  │   │
│  │ Detection: trimmed query has no spaces       │      │ Detection: trimmed query has     │   │
│  │                                              │      │ one or more spaces               │   │
│  │ Chroma payload adds:                         │      │                                  │   │
│  │ where_document: { "$contains": rawQuery }    │      │ Chroma payload uses raw vector    │   │
│  │                                              │      │ search with no document filter    │   │
│  └──────────────────┬───────────────────────────┘      └────────────────┬────────────────┘   │
│                     │                                                     │                    │
│                     └──────────────────────┬──────────────────────────────┘                    │
│                                            ▼                                                   │
│                           Dynamic fallback: retry semantic search if                           │
│                           the short-keyword document filter returns no hits                     │
└────────────────────────────────────────────┬───────────────────────────────────────────────────┘
                                             │
                                             ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│  3. Database / Retrieval Layer                                                               │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                              │
│  ┌──────────────────────────────────────────────┐      ┌─────────────────────────────────┐   │
│  │ MongoDB                                      │      │ ChromaDB                         │   │
│  │ Source of Truth                              │      │ Vector Retrieval Store           │   │
│  │                                              │      │                                  │   │
│  │ Used by: seed/main.go, ingest/main.go        │      │ Used by: ingest/main.go,          │   │
│  │ Database: content_db                         │      │ retrieve/main.go                 │   │
│  │ Collection: articles_v2                      │      │ Collection: policies_v2           │   │
│  │                                              │      │                                  │   │
│  │ Owns canonical source text for ingestion.    │      │ Stores document text plus vectors │   │
│  │ Retrieval does not query MongoDB at runtime. │      │ and returns up to 2 candidates.   │   │
│  └──────────────────────────────────────────────┘      └─────────────────────────────────┘   │
│                                                                                              │
│  Chroma query payloads                                                                        │
│                                                                                              │
│  Short keyword:                                                                               │
│  { "query_embeddings": [[...]], "n_results": 2, "where_document": { "$contains": "health" } } │
│                                                                                              │
│  Complex phrase or fallback:                                                                  │
│  { "query_embeddings": [[...]], "n_results": 2 }                                              │
└────────────────────────────────────────────┬─────────────────────────────────────────────────┘
                                             │
                                             ▼
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│  4. LLM Generation Layer                                                                     │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────────────────────┐    │
│  │ Prompt Assembly                                                                      │    │
│  │                                                                                      │    │
│  │ "You are a precise corporate assistant. Answer the query based ONLY on the provided   │    │
│  │ context retrieved from our database. If you cannot answer it, say                    │    │
│  │ I cannot answer this based on the provided information."                             │    │
│  │                                                                                      │    │
│  │ Format Requirement: prose | Markdown table | JSON object                             │    │
│  │ Context: combined Chroma documents                                                   │    │
│  │ Query: original user query                                                           │    │
│  └──────────────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                              │
│  ┌──────────────────────────────────────────────┐      ┌─────────────────────────────────┐   │
│  │ Anthropic Claude Client                      │      │ Ollama Local Client              │   │
│  │                                              │      │                                  │   │
│  │ Used when ANTHROPIC_API_KEY is set           │      │ Used when ANTHROPIC_API_KEY      │   │
│  │ Model: claude-3-5-sonnet-20241022            │      │ is not set                       │   │
│  │ Endpoint: https://api.anthropic.com/v1       │      │ Model: gemma4:e2b                │   │
│  │ max_tokens: 1024                             │      │ Endpoint: /api/generate          │   │
│  └──────────────────┬───────────────────────────┘      └────────────────┬────────────────┘   │
│                     │                                                     │                    │
│                     └──────────────────────┬──────────────────────────────┘                    │
│                                            ▼                                                   │
│                           ┌──────────────────────────────────────────────┐                     │
│                           │ JSON API Response                            │                     │
│                           │                                              │                     │
│                           │ - answer                                     │                     │
│                           │ - source                                     │                     │
│                           │ - score                                      │                     │
│                           │ - generator                                  │                     │
│                           └──────────────────────────────────────────────┘                     │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

### Key Engineering Features:
* **Zero-Data Alteration:** Works directly on raw, unpadded enterprise text segments.
* **Deterministic Keyword Protection:** Uses ChromaDB document pre-filtering (`where_document` constraints) to reduce embedding drift on short-tail keywords.
* **Dynamic Failure Fallback:** Automatically drops strict constraints and shifts to pure semantic vector math if zero string matches are found—ensuring high recall.
* **Go Backend Performance:** Lightweight, direct HTTP services with a small orchestration surface.

---

## 🛠️ Tech Stack

* **Language:** Go (Golang)
* **Primary Store:** MongoDB (Source of Truth)
* **Vector Database:** ChromaDB
* **Embeddings Model:** `nomic-embed-text` (via Ollama)
* **Generation Models:** Claude 3.5 Sonnet / Gemma 4 e2b
* **Current Implementation:** `chromadb-rag/`
* **Legacy Prototype:** `mongo-rag-beginner/`

---

## 🚦 Getting Started

### 1. Prerequisites
Ensure you have MongoDB, ChromaDB, and Ollama running locally in your development environment.

```bash
# Pull the local embedding model
ollama pull nomic-embed-text

# Pull the local fallback generation model
ollama pull gemma4:e2b
```

### 2. Environment Variables

Set up your environment keys before launching the application:

```bash
export ANTHROPIC_API_KEY="your-api-key" # Optional, falls back to local Ollama if empty
export MONGO_URI="mongodb://localhost:27017"
```

The current code also uses these hard-coded local endpoints:

```text
MongoDB:  mongodb://localhost:27017 unless MONGO_URI is set
ChromaDB: http://localhost:8000
Ollama:   http://localhost:11434
Server:   http://localhost:8080
```

### 3. Run the Engine

```bash
# From the repository root
cd chromadb-rag

# Seed MongoDB source content
go run seed/main.go

# Ingest MongoDB content into ChromaDB
go run ingest/main.go

# Start the retrieval web server and API
go run retrieve/main.go
```

The retrieval web server will launch immediately on `http://localhost:8080`.

---

## 🔌 API Usage

### POST `/api/query`

Runs query embedding, ChromaDB retrieval, prompt construction, and LLM generation.

Request body:

```json
{
  "query": "health",
  "format": "prose"
}
```

Supported `format` values:

```text
prose
table
json
```

Example:

```bash
curl -X POST http://localhost:8080/api/query \
  -H "Content-Type: application/json" \
  -d '{
    "query": "health",
    "format": "prose"
  }'
```

Response shape:

```json
{
  "answer": "Health insurance benefits are fully covered for all permanent full-time employees beginning on their first day of employment.",
  "source": "- Comprehensive Healthcare and Medical Benefits Package: Group health insurance benefits, including dental and vision insurance coverage, are fully paid and covered for all permanent full-time employees, beginning immediately on their official first day of employment.\n",
  "score": 0.9281,
  "generator": "Claude 3.5 Sonnet"
}
```

If `ANTHROPIC_API_KEY` is not set, `generator` is returned as:

```text
Ollama (Gemma 4 e2b)
```

### CORS

`OPTIONS /api/query` is supported for browser preflight requests. The handler sets:

```text
Access-Control-Allow-Origin: *
Access-Control-Allow-Headers: Content-Type
Access-Control-Allow-Methods: POST, OPTIONS
```

### Health Checks and CLI Flags

The current code does not implement a dedicated `GET /health` endpoint or CLI flags. Runtime values such as ChromaDB URL, Ollama URL, model names, and server port are currently hard-coded in the Go files.

---

## 📂 Recommended Directory Structure

The current repository structure is:

```text
Omni-RAG/
├── README.md
├── README_v2.md
├── LICENSE
├── chromadb-rag/
│   ├── README.md
│   ├── go.mod
│   ├── go.sum
│   ├── seed/
│   │   └── main.go
│   ├── ingest/
│   │   └── main.go
│   └── retrieve/
│       ├── main.go
│       └── static/
│           ├── app.js
│           ├── index.html
│           └── style.css
└── mongo-rag-beginner/
    ├── README.md
    ├── go.mod
    ├── go.sum
    ├── ingest/
    │   └── main.go
    └── retrieve/
        ├── main.go
        └── static/
            ├── app.js
            ├── index.html
            └── style.css
```

`chromadb-rag/` is the current OmniRAG implementation. `mongo-rag-beginner/` is an earlier MongoDB-only prototype that stores embeddings directly in MongoDB and performs an in-process dot-product scan.

---

## 🗺️ Engineering Roadmap

OmniRAG is actively evolving from a single-node retrieval pipeline into a highly distributed, multi-agent framework.

* [x] **v1.0.0 (Current):** Go + ChromaDB metadata optimization layer, fixing short-query vector bias.
* [ ] **v1.1.0:** Abstracted multi-vector DB client interface supporting **Qdrant** and **Milvus**.
* [ ] **v1.2.0:** Horizontally scalable ingestion worker pools for heavy concurrent document parsing.
* [ ] **v2.0.0:** Integration of **LangChain** and **LangGraph** for autonomous, stateful multi-agent workflows and graph-based context retrieval.

---

## 📄 License

Distributed under the MIT License. See `LICENSE` for more information.
