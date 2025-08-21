package types

import (
	"encoding/json"
)

// Message представляет сообщение в чате
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMRequest определяет структуру запроса для генерации суммаризации или рекомендации
type LLMRequest struct {
	CardID   string    `json:"card_id"`
	Messages []Message `json:"messages"`
}

// LLMResponse определяет структуру ответа для суммаризации
type LLMResponse struct {
	CardID   string `json:"card_id"`
	Summary  string `json:"summary"`
	Solution string `json:"solution"`
	Category string `json:"category"`
	Error    string `json:"error,omitempty"`
}

// EmbeddingRequest определяет структуру запроса для генерации эмбеддинга
type EmbeddingRequest struct {
	SourceID   string `json:"source_id"`
	SourceType string `json:"source_type"`
	Text       string `json:"text"`
}

// EmbeddingResponse определяет структуру ответа с эмбеддингом
type EmbeddingResponse struct {
	SourceID   string    `json:"source_id"`
	SourceType string    `json:"source_type"`
	Embedding  []float32 `json:"embedding"`
	Text       string    `json:"text"`
	Error      string    `json:"error,omitempty"`
}

// LLMTask определяет универсальную структуру запроса для LLM-задач
type LLMTask struct {
	RequestID     string      `json:"request_id"`
	CorrelationID string      `json:"correlation_id"`
	Source        string      `json:"source"`
	Type          string      `json:"type"`
	Payload       interface{} `json:"payload"`
}

// LLMResult определяет универсальную структуру результата LLM-задач
type LLMResult struct {
	RequestID     string          `json:"request_id"`
	CorrelationID string          `json:"correlation_id"`
	Source        string          `json:"source"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
}
