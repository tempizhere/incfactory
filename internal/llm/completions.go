package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tempizhere/incfactory/internal/types"

	"go.uber.org/zap"
)

// CompletionRequestOpenAI определяет структуру запроса для /openai-proxy/v1/chat/completions
type CompletionRequestOpenAI struct {
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	Messages         []types.Message   `json:"messages"`
	FrequencyPenalty float32           `json:"frequency_penalty"`
	MaxTokens        int               `json:"max_tokens"`
	N                int               `json:"n"`
	PresencePenalty  float32           `json:"presence_penalty"`
	Seed             int               `json:"seed"`
	Temperature      float32           `json:"temperature"`
	TopP             float32           `json:"top_p"`
	Stream           bool              `json:"stream"`
	ResponseFormat   map[string]string `json:"response_format,omitempty"`
	Stop             []string          `json:"stop,omitempty"`
	LogitBias        map[string]int    `json:"logit_bias,omitempty"`
}

// CompletionRequestDirectLLM определяет структуру запроса для /direct-llm/v1/chat/completions
type CompletionRequestDirectLLM struct {
	RequestBody struct {
		Provider         string          `json:"provider"`
		Model            string          `json:"model"`
		Messages         []types.Message `json:"messages"`
		FrequencyPenalty float32         `json:"frequency_penalty"`
		MaxTokens        int             `json:"max_tokens"`
		N                int             `json:"n"`
		PresencePenalty  float32         `json:"presence_penalty"`
		Seed             int             `json:"seed"`
		Temperature      float32         `json:"temperature"`
		TopP             float32         `json:"top_p"`
		Stream           bool            `json:"stream"`
		Stop             []string        `json:"stop,omitempty"`
		LogitBias        map[string]int  `json:"logit_bias,omitempty"`
	} `json:"request_body"`
}

// CompletionResponseOpenAI определяет структуру ответа от /openai-proxy/v1/chat/completions
type CompletionResponseOpenAI struct {
	Choices []struct {
		Index        int           `json:"index"`
		Message      types.Message `json:"message"`
		FinishReason string        `json:"finish_reason"`
		StopReason   interface{}   `json:"stop_reason"`
	} `json:"choices"`
	Created string `json:"created"`
	ID      string `json:"id"`
	Model   string `json:"model"`
	Object  string `json:"object"`
	Usage   struct {
		CompletionTokens int `json:"completion_tokens"`
		PromptTokens     int `json:"prompt_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// CompletionResponseDirectLLM определяет структуру ответа от /direct-llm/v1/chat/completions
type CompletionResponseDirectLLM struct {
	DateTime   string `json:"date_time"`
	Mask       bool   `json:"mask"`
	ResultBody struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Usage    struct {
			CompletionTokens int `json:"completion_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		ResultType string `json:"result_type"`
		Choices    []struct {
			Index        int           `json:"index"`
			Message      types.Message `json:"message"`
			FinishReason string        `json:"finish_reason"`
		} `json:"choices"`
		Citations interface{} `json:"citations"`
	} `json:"result_body"`
}

// GenerateCompletion генерирует текстовое завершение на основе сообщений
func GenerateCompletion(messages []types.Message, temperature float64, maxTokensLimit int, topP, frequencyPenalty, presencePenalty float32) (string, error) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Вычисляем max_tokens
	totalLength := 0
	for _, msg := range messages {
		totalLength += len(msg.Content)
	}
	maxTokens := 10000
	if totalLength > 0 {
		calculatedTokens := int(float64(totalLength) * 1.5)
		if calculatedTokens > maxTokensLimit {
			calculatedTokens = maxTokensLimit
		}
		if calculatedTokens > maxTokens {
			maxTokens = calculatedTokens
		}
	}

	// Определяем, использовать ли Direct-LLM
	useDirectLLM := os.Getenv("DIRECT_LLM") == "TRUE"
	var body []byte
	var url string
	var err error

	if useDirectLLM {
		// Формируем тело запроса для Direct-LLM
		reqBody := CompletionRequestDirectLLM{}
		reqBody.RequestBody.Provider = completionProvider
		reqBody.RequestBody.Model = completionModel
		reqBody.RequestBody.Messages = messages
		reqBody.RequestBody.FrequencyPenalty = frequencyPenalty
		reqBody.RequestBody.MaxTokens = maxTokens
		reqBody.RequestBody.N = 1
		reqBody.RequestBody.PresencePenalty = presencePenalty
		reqBody.RequestBody.Seed = 12
		reqBody.RequestBody.Temperature = float32(temperature)
		reqBody.RequestBody.TopP = topP
		reqBody.RequestBody.Stream = false
		reqBody.RequestBody.Stop = []string{"\n"}
		reqBody.RequestBody.LogitBias = map[string]int{"22": 0}
		body, err = json.Marshal(reqBody)
		url = fmt.Sprintf("%s/direct-llm/v1/chat/completions", completionHost)
	} else {
		// Формируем тело запроса для OpenAI-like
		reqBody := CompletionRequestOpenAI{
			Provider:         completionProvider,
			Model:            completionModel,
			Messages:         messages,
			FrequencyPenalty: frequencyPenalty,
			MaxTokens:        maxTokens,
			N:                1,
			PresencePenalty:  presencePenalty,
			Seed:             12,
			Temperature:      float32(temperature),
			TopP:             topP,
			Stream:           false,
			ResponseFormat:   map[string]string{"type": "json_object"},
			Stop:             []string{"\n"},
			LogitBias:        map[string]int{"22": 0},
		}
		body, err = json.Marshal(reqBody)
		url = fmt.Sprintf("%s/v1/chat/completions", completionHost)
	}

	if err != nil {
		logger.Error("Ошибка сериализации запроса", zap.Error(err))
		return "", fmt.Errorf("ошибка сериализации: %w", err)
	}
	logger.Info("Отправка запроса на генерацию текста", zap.ByteString("body", body))

	for attempt := 1; attempt <= 3; attempt++ {
		// Создаём HTTP-запрос
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			logger.Error("Ошибка создания HTTP-запроса", zap.Error(err), zap.Int("attempt", attempt))
			return "", fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if completionAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+completionAPIKey)
		}

		// Выполняем запрос
		resp, err := completionClient.Do(req)
		if err != nil {
			if attempt == 3 {
				logger.Error("Ошибка выполнения запроса после 3 попыток", zap.Error(err))
				return "", fmt.Errorf("ошибка выполнения запроса после 3 попыток: %w", err)
			}
			logger.Warn("Ошибка попытки, будет повтор", zap.Int("attempt", attempt), zap.Error(err))
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		defer resp.Body.Close()

		// Проверяем статус-код ответа
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			if attempt == 3 {
				logger.Error("Ошибка API после 3 попыток",
					zap.Int("status_code", resp.StatusCode),
					zap.String("response", string(body)))
				return "", fmt.Errorf("ошибка LLM API (%d) после 3 попыток: %s", resp.StatusCode, string(body))
			}
			logger.Warn("Ошибка API, будет повтор",
				zap.Int("attempt", attempt),
				zap.Int("status_code", resp.StatusCode),
				zap.String("response", string(body)))
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		// Декодируем ответ
		var content string
		if useDirectLLM {
			var compResp CompletionResponseDirectLLM
			if err := json.NewDecoder(resp.Body).Decode(&compResp); err != nil {
				logger.Error("Ошибка декодирования ответа", zap.Error(err))
				return "", fmt.Errorf("ошибка декодирования: %w", err)
			}
			if len(compResp.ResultBody.Choices) == 0 {
				logger.Error("Нет ответа от модели")
				return "", fmt.Errorf("нет ответа от модели")
			}
			content = compResp.ResultBody.Choices[0].Message.Content
		} else {
			var compResp CompletionResponseOpenAI
			if err := json.NewDecoder(resp.Body).Decode(&compResp); err != nil {
				logger.Error("Ошибка декодирования ответа", zap.Error(err))
				return "", fmt.Errorf("ошибка декодирования: %w", err)
			}
			if len(compResp.Choices) == 0 {
				logger.Error("Нет ответа от модели")
				return "", fmt.Errorf("нет ответа от модели")
			}
			content = compResp.Choices[0].Message.Content
		}

		logger.Info("Успешно получен ответ", zap.String("content", content))
		return content, nil
	}
	return "", fmt.Errorf("не удалось выполнить запрос после 3 попыток")
}
