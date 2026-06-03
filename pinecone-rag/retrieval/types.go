package main

// SearchRequest is the incoming query payload.
type SearchRequest struct {
	Query string `json:"query"`
}

// SourceMatch is a single document chunk returned from Pinecone.
type SourceMatch struct {
	ID           string  `json:"id"`
	Score        float64 `json:"score"`
	SourceFileID string  `json:"source_file_id"`
	TextContent  string  `json:"text_content"`
	Chapter      int     `json:"chapter"`
	PageNumber   int     `json:"page_number"`
}

// SSE event payloads.

type stageEvent struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type tokenEvent struct {
	Text string `json:"text"`
}

type sourcesEvent struct {
	Sources []SourceMatch `json:"sources"`
}

type errorEvent struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}
