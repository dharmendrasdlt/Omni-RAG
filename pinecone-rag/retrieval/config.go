package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port            string
	OllamaBaseURL   string
	OllamaModel     string
	GenerationModel string
	PineconeAPIKey  string
	PineconeHost    string
	PineconeIndex   string
	PineconeNS      string
	TopK            int
	AnthropicAPIKey     string
	AnthropicModel      string
	AnthropicHasCredits bool
}

type PineconeJSONConfig struct {
	PineconeAPIKey    string `json:"PINECONE_API_KEY"`
	PineconeIndexName string `json:"PINECONE_INDEX_NAME"`
	PineconeHost      string `json:"PINECONE_HOST"`
	PineconeNamespace string `json:"PINECONE_NAMESPACE"`
	EmbeddingModel    string `json:"EMBEDDING_MODEL"`
	GenerationModel   string `json:"GENERATION_MODEL"`
	AnthropicAPIKey     string `json:"ANTHROPIC_API_KEY"`
	AnthropicModel      string `json:"ANTHROPIC_MODEL"`
	AnthropicHasCredits bool   `json:"ANTHROPIC_CREDIT_BALANCE"`
}

func LoadPineconeJSON(path string) PineconeJSONConfig {
	var config PineconeJSONConfig
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	if err := json.Unmarshal(fileBytes, &config); err != nil {
		log.Printf("Warning: Found pinecone-config.json but failed to parse it: %v", err)
	}
	return config
}

func loadConfig() Config {
	jsonCfg := LoadPineconeJSON("../config.json")
	return Config{
		Port:          env("PORT", "8081"),
		OllamaBaseURL: strings.TrimRight(env("OLLAMA_BASE_URL", "http://localhost:11434"), "/"),

		OllamaModel:     envOrJSON(env("EMBEDDING_MODEL", ""), jsonCfg.EmbeddingModel, "gemma4:e2b"),
		GenerationModel: envOrJSON(env("GENERATION_MODEL", ""), jsonCfg.GenerationModel, "gemma4:e4b"),

		PineconeAPIKey: envOrJSON(os.Getenv("PINECONE_API_KEY"), jsonCfg.PineconeAPIKey, ""),
		PineconeHost:   strings.TrimRight(envOrJSON(os.Getenv("PINECONE_HOST"), jsonCfg.PineconeHost, ""), "/"),
		PineconeIndex:  envOrJSON(os.Getenv("PINECONE_INDEX"), jsonCfg.PineconeIndexName, ""),
		PineconeNS:     envOrJSON(os.Getenv("PINECONE_NAMESPACE"), jsonCfg.PineconeNamespace, ""),

		TopK: envInt("RETRIEVAL_TOP_K", 3),

		AnthropicAPIKey:     envOrJSON(os.Getenv("ANTHROPIC_API_KEY"), jsonCfg.AnthropicAPIKey, ""),
		AnthropicModel:      envOrJSON(env("ANTHROPIC_MODEL", ""), jsonCfg.AnthropicModel, ""),
		AnthropicHasCredits: jsonCfg.AnthropicHasCredits,
	}
}

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
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
