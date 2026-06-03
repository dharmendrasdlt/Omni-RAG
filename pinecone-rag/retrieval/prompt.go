package main

import (
	"fmt"
	"strings"
)

// buildRAGPrompt returns the system context and the user question separately
// so each streamer can format them as its API requires.
func buildRAGPrompt(query string, sources []SourceMatch) (system, user string) {
	var sb strings.Builder
	sb.WriteString("You are a technical assistant. ")
	sb.WriteString("Answer the user's question using ONLY the context block provided below. ")
	sb.WriteString("If the answer cannot be found in the context, state clearly that you do not know. ")
	sb.WriteString("Do not make up facts.\n\n")
	sb.WriteString("[Context Content]:\n")
	for i, s := range sources {
		sb.WriteString(fmt.Sprintf("--- Source %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("Document ID: %s | Chapter: %d | Page: %d\n", s.SourceFileID, s.Chapter, s.PageNumber))
		sb.WriteString(fmt.Sprintf("Content: %q\n", s.TextContent))
		sb.WriteString("----------------------\n")
	}
	return sb.String(), query
}
