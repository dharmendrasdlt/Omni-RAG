# Flexible Go RAG with separate Ingestion and Retrieval Services

A highly extensible, modular microservices RAG (Retrieval-Augmented Generation) pipeline written in Go.
It utilizes a MongoDB database as a vector/knowledge store, supports local embedding generation (Ollama), and integrates with **Claude (Anthropic API)** or a **local LLM (Ollama)** for answering queries with configurable formatting options (Prose, Table, JSON) via a beautiful search engine UI.

## Architecture

```
mongo-rag/
├── ingest/
│   └── main.go       # Ingestion Service: Embeds & stores knowledge documents.
└── retrieve/
    ├── main.go       # Retrieval HTTP Server: Performs query embedding, DB lookups & LLM prompting.
    └── static/       # Search Engine UI Frontend (HTML, CSS, JS)
```

---

## Prerequisites

1. **MongoDB**: Have a running MongoDB service (Local or Atlas cluster).
   - Default fallback: `mongodb://localhost:27017`
   - Custom configuration: Set `MONGO_URI` environment variable.
2. **Ollama**: Install and start Ollama locally. Pull the local embedding model:
   ```bash
   ollama pull nomic-embed-text
   ollama pull llama3
   ```
3. **Claude API (Optional)**: If you want to use Anthropic's Claude 3.5 Sonnet for generation, export your API key:
   ```bash
   export ANTHROPIC_API_KEY="your-api-key"
   ```
   If not set, the pipeline automatically falls back to your local Ollama `llama3` instance!

---

## How to Run

### Step 1: Ingest Data
Before searching, load raw knowledge context and their embeddings into the MongoDB collection. Run the ingestion microservice:

```bash
cd mongo-rag
go run ingest/main.go
```

You should see output confirming documents were embedded and stored.

### Step 2: Start the Search Engine
Launch the Retrieval server hosting both the web server and query execution APIs:

```bash
go run retrieve/main.go
```

The service will output:
`Retrieval Service + Search UI listening on http://localhost:8080`

### Step 3: Query via Web UI
Open your browser and navigate to **`http://localhost:8080`**.

1. Enter a query in the Google-style text bar (e.g. `"What are the remote work rules?"` or `"When do benefits start?"`).
2. Select your desired output format: **Human Prose**, **Markdown Table**, or **Structured JSON**.
3. Press **Enter** or click **Search** to execute the vector retrieval and generation pipeline!
