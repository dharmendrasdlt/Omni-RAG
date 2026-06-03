package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// Streamer generates a streaming LLM response from a system context and user question.
type Streamer interface {
	Stream(ctx context.Context, system, user string, onToken func(string) error) error
	ModelName() string
}

// ─── Ollama ───────────────────────────────────────────────────────────────────

type OllamaStreamer struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

func (o *OllamaStreamer) ModelName() string { return o.Model }

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateChunk struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func (o *OllamaStreamer) Stream(ctx context.Context, system, user string, onToken func(string) error) error {
	prompt := "[System Instruction]: " + system + "\n[User Question]: " + user + "\n"

	body, err := json.Marshal(ollamaGenerateRequest{Model: o.Model, Prompt: prompt, Stream: true})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama generate request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close ollama generate response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ollama generate status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaGenerateChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			log.Printf("parse ollama chunk: %v (line: %q)", err, line)
			continue
		}
		if chunk.Response != "" {
			if err := onToken(chunk.Response); err != nil {
				return err
			}
		}
		if chunk.Done {
			break
		}
	}
	return scanner.Err()
}

// ─── Anthropic ────────────────────────────────────────────────────────────────

type AnthropicStreamer struct {
	APIKey string
	Model  string
	Client *http.Client
}

func (a *AnthropicStreamer) ModelName() string { return a.Model }

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicStreamRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
}

// Stream calls the Anthropic Messages API with streaming and delivers each
// text_delta token to onToken.
func (a *AnthropicStreamer) Stream(ctx context.Context, system, user string, onToken func(string) error) error {
	payload := anthropicStreamRequest{
		Model:     a.Model,
		MaxTokens: 1024,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
		Stream:    true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("close anthropic response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	// Anthropic SSE: lines alternate between "event: <type>" and "data: <json>".
	// We only extract text from content_block_delta events.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") && currentEvent == "content_block_delta" {
			raw := strings.TrimPrefix(line, "data: ")
			var delta struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(raw), &delta); err != nil {
				log.Printf("parse anthropic delta: %v (data: %q)", err, raw)
				continue
			}
			if delta.Delta.Type == "text_delta" && delta.Delta.Text != "" {
				if err := onToken(delta.Delta.Text); err != nil {
					return err
				}
			}
		}

		if line == "" {
			currentEvent = ""
		}
	}
	return scanner.Err()
}
