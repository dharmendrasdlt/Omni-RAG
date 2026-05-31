# OmniRAG 🚀

**OmniRAG** is a high-performance, enterprise-grade Retrieval-Augmented Generation (RAG) engine written in Go. 

Unlike standard out-of-the-box RAG setups that fall apart on real-world data, OmniRAG is built to work *on top of existing data* without forcing database schema migrations, intrusive backend re-indexing, or requiring end-users to master complex prompt engineering. It handles localized short keyword searches and abstract semantic queries with equal precision by shifting retrieval intelligence entirely to the orchestration layer.

---

## 🏗️ Core Architecture

OmniRAG isolates the database layer from the AI retrieval logic. It treats your primary database (e.g., MongoDB) as an immutable source of truth, while using advanced metadata routing and conditional fallback mechanisms inside the vector layer (ChromaDB) to guarantee deterministic accuracy.


```

```
              ┌──────────────────────────────────────────────┐
              │            User Query: "health"              │
              └──────────────────────┬───────────────────────┘
                                     │
                                     ▼
                    ┌──────────────────────────────────┐
                    │      OmniRAG Router Layer        │
                    └────────────────┬─────────────────┘
                                     │
                ┌────────────────────┴────────────────────┐
                │ [Keyword Spike Detected]                │ [Complex Phrase]
                ▼                                         ▼
   ┌──────────────────────────┐              ┌──────────────────────────┐
   │ Chroma Metadata Filter   │              │   Raw Semantic Search    │
   │  • Using `$contains`     │              │  • Vector cluster match  │
   │  • Skips bad vector bias │              │  • Handles abstract logic│
   └────────────┬─────────────┘              └────────────┬─────────────┘
                │                                         │
                └────────────────────┬────────────────────┘
                                     │
                                     ▼
              ┌──────────────────────────────────────────────┐
              │    Perfect Context Injected into LLM Window  │
              └──────────────────────────────────────────────┘

```

```

### Key Engineering Features:
* **Zero-Data Alteration:** Works directly on raw, unpadded enterprise text segments.
* **Deterministic Keyword Protection:** Uses native vector database metadata pre-filtering (`where_document` constraints) to eliminate embedding drift on short-tail keywords.
* **Dynamic Failure Fallback:** Automatically drops strict constraints and shifts to pure semantic vector math if zero string matches are found—ensuring high recall.
* **Go Backend Performance:** Lightweight, ultra-fast concurrent execution pipeline.

---

## 🛠️ Tech Stack

* **Language:** Go (Golang)
* **Primary Store:** MongoDB (Source of Truth)
* **Vector Database:** ChromaDB
* **Embeddings Model:** `nomic-embed-text` (via Ollama)
* **Generation Models:** Claude 3.5 Sonnet / Gemma 4 e2b

---

## 🚦 Getting Started

### 1. Prerequisites
Ensure you have MongoDB, ChromaDB, and Ollama running locally in your development environment.
```bash
# Start Ollama with the appropriate models loaded
ollama run nomic-embed-text
ollama run gemma4:e2b

```

### 2. Environment Variables

Set up your environment keys before launching the application:

```bash
export ANTHROPIC_API_KEY="your-api-key" # Optional, falls back to local Ollama if empty
export MONGO_URI="mongodb://localhost:27017"

```

### 3. Run the Engine

```bash
go run main.go

```

The retrieval web server will launch immediately on `http://localhost:8080`.

---

## 📂 Recommended Directory Structure

As this project scales out to multiple databases and agentic frameworks, the repository maintains an isolated, clean separation of concerns:

```text
├── clients/          # Abstracted DB connectors (Chroma, Mongo, Qdrant)
├── controllers/      # HTTP handlers and server orchestration logic
├── engines/          # Strategy pattern logic (Vector, Keyword, Hybrid)
├── graphs/           # Future home for LangGraph / Multi-agent workflows
├── static/           # UI frontend files
├── main.go           # Server entrypoint
└── README.md         # Documentation

```

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