# Retrieval Service — SSE Stream Format

`POST /api/search` on the retrieval service (port `8081`) responds with a
Server-Sent Events stream rather than a single JSON body. This document
describes the exact wire format, every event type, real payload examples,
edge cases, and how the code emits and parses them.

## Response headers

```text
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

## Wire format

Each event is written by `sseWrite` (in `retrieval/server.go`) as:

```text
event: <eventType>\n
data: <json-payload>\n
\n
```

i.e. an `event:` line, a `data:` line containing a JSON object, and a blank
line terminator. The stream is flushed after every event.

## Event sequence (happy path)

```text
event: stage     data: {"stage":"embedding","message":"Generating query embedding via Ollama…"}
event: stage     data: {"stage":"retrieval","message":"Querying Pinecone for top 3 matches…"}
event: stage     data: {"stage":"generating","message":"Generating answer with claude-haiku-4-5-20251001…"}
event: token     data: {"text":"The "}
event: token     data: {"text":"Night's "}
event: token     data: {"text":"Watch "}
...
event: sources   data: {"sources":[ ... ]}
event: done      data: {"ok":true}
```

Tokens stream incrementally during generation. `sources` and `done` are sent
once, after generation completes successfully.

## Event types

### `stage`
Progress markers for each pipeline step. Payload: `stageEvent`.

| Field | Type | Description |
|---|---|---|
| `stage` | string | One of `embedding`, `retrieval`, `generating` |
| `message` | string | Human-readable status for the UI |

### `token`
A chunk of generated answer text. Payload: `tokenEvent`. Emitted many times.

| Field | Type | Description |
|---|---|---|
| `text` | string | Partial answer text — concatenate in arrival order |

### `sources`
The retrieved Pinecone matches that grounded the answer. Payload:
`sourcesEvent` — `{ "sources": [SourceMatch, ...] }`.

`SourceMatch` fields:

| Field | Type | Description |
|---|---|---|
| `id` | string | Pinecone vector id (`book_<mongoId>_chunk_<n>`) |
| `score` | float | Similarity score |
| `source_file_id` | string | GridFS ObjectID of the source PDF |
| `text_content` | string | The matched chunk text |
| `chapter` | int | Chapter (defaults to `1`) |
| `page_number` | int | Page the chunk came from |

Example:
```json
{
  "sources": [
    {
      "id": "book_665a1f_chunk_42",
      "score": 0.83,
      "source_file_id": "665a1f...",
      "text_content": "The Night's Watch is a military order...",
      "chapter": 1,
      "page_number": 88
    }
  ]
}
```

### `done`
Terminal success marker. Payload: `{ "ok": true }`. The stream ends after this.

### `error`
Emitted instead of continuing when any step fails. Payload: `errorEvent`.
The stream ends after an error event (no `done` follows).

| Field | Type | Description |
|---|---|---|
| `stage` | string | Where it failed: `embedding`, `retrieval`, or `generating` |
| `message` | string | Error detail |

## Edge cases

### No matching documents
If Pinecone returns zero matches, the service emits an `error` at the
`retrieval` stage and stops — no tokens, no sources:

```text
event: stage   data: {"stage":"retrieval","message":"Querying Pinecone for top 3 matches…"}
event: error   data: {"stage":"retrieval","message":"No matching documents found in the knowledge base for this query."}
```

### Embedding failure (Ollama down)
```text
event: error   data: {"stage":"embedding","message":"Embedding failed: ..."}
```

### Generation failure (e.g. Anthropic out of credits)
The Anthropic API returns HTTP 400 when the account balance is too low; this
surfaces as a `generating` error:

```text
event: error   data: {"stage":"generating","message":"LLM streaming failed: anthropic status 400: {... credit balance is too low ...}"}
```

To avoid this, set `ANTHROPIC_CREDIT_BALANCE: false` in `config.json` to force
the Ollama fallback.

### Bad request (not SSE)
Validation errors before the stream starts are returned as a normal JSON body
with the appropriate HTTP status (not SSE):

```json
{ "error": "query must not be empty" }
```

## How the code handles it

- **Server** (`retrieval/server.go`): `handleSearch` writes each event via
  `sseWrite`. Generation tokens come through the `Streamer.Stream` callback.
- **Anthropic streamer** (`retrieval/streamer.go`): parses Anthropic's own SSE
  (`event: content_block_delta` → `delta.text`) and forwards each `text_delta`
  as a `token` event.
- **Ollama streamer**: reads newline-delimited JSON chunks from
  `/api/generate` and forwards each `response` field as a `token` event.
- **Client/UI**: should switch on the `event:` line, JSON-parse `data:`, append
  `token.text` to the answer, render `sources` when received, and treat `error`
  as terminal.
