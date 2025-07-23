package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/tempizhere/incfactory/internal/types"
	"go.uber.org/zap"
)

// EmbeddingRequestOpenAI определяет структуру запроса для /openai-proxy/v1/embeddings
type EmbeddingRequestOpenAI struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Input    []string `json:"input"`
}

// EmbeddingRequestDirectLLM определяет структуру запроса для /direct-llm/v1/embeddings
type EmbeddingRequestDirectLLM struct {
	RequestBody struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Texts    string `json:"texts"`
	} `json:"request_body"`
}

// EmbeddingResponseOpenAI определяет структуру ответа от /openai-proxy/v1/embeddings
type EmbeddingResponseOpenAI struct {
	Object string `json:"object"`
	Model  string `json:"model"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens     int  `json:"prompt_tokens"`
		TotalTokens      int  `json:"total_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

// EmbeddingResponseDirectLLM определяет структуру ответа от /direct-llm/v1/embeddings
type EmbeddingResponseDirectLLM struct {
	DateTime   string `json:"date_time"`
	Mask       bool   `json:"mask"`
	ResultBody struct {
		Provider   string      `json:"provider"`
		Model      string      `json:"model"`
		ResultType string      `json:"result_type"`
		Embeddings [][]float32 `json:"embeddings"`
		Usage      struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result_body"`
}

// GenerateEmbedding генерирует эмбеддинг для заданного запроса
func GenerateEmbedding(req types.EmbeddingRequest) ([]float32, error) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Обрезаем текст до максимальной длины
	text := req.Text
	if embeddingMaxTextLength > 0 && len(text) > embeddingMaxTextLength {
		logger.Info("Обрезка текста для эмбеддинга",
			zap.Int("original_length", len(text)),
			zap.Int("max_length", embeddingMaxTextLength))
		text = text[:embeddingMaxTextLength]
	}

	// Определяем, использовать ли Direct-LLM
	useDirectLLM := os.Getenv("DIRECT_LLM") == "TRUE"
	var body []byte
	var url string
	var err error

	if useDirectLLM {
		// Формируем тело запроса для Direct-LLM
		data := EmbeddingRequestDirectLLM{}
		data.RequestBody.Provider = embeddingProvider
		data.RequestBody.Model = embeddingModel
		data.RequestBody.Texts = text
		body, err = json.Marshal(data)
		url = fmt.Sprintf("%s/direct-llm/v1/embeddings", embeddingHost)
	} else {
		// Формируем тело запроса для OpenAI-like
		data := EmbeddingRequestOpenAI{
			Provider: embeddingProvider,
			Model:    embeddingModel,
			Input:    []string{text},
		}
		body, err = json.Marshal(data)
		url = fmt.Sprintf("%s/v1/embeddings", embeddingHost)
	}

	if err != nil {
		logger.Error("Ошибка сериализации запроса", zap.Error(err))
		return nil, fmt.Errorf("ошибка сериализации: %w", err)
	}

	for attempt := 1; attempt <= embeddingMaxRetries; attempt++ {
		// Создаём HTTP-запрос
		httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			logger.Error("Ошибка создания HTTP-запроса", zap.Error(err), zap.Int("attempt", attempt))
			return nil, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		if embeddingAPIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+embeddingAPIKey)
		}

		// Выполняем запрос
		resp, err := embeddingClient.Do(httpReq)
		if err != nil {
			if attempt == embeddingMaxRetries {
				logger.Error("Ошибка выполнения запроса после всех попыток", zap.Error(err), zap.Int("attempt", attempt))
				return nil, fmt.Errorf("ошибка выполнения запроса после %d попыток: %v", embeddingMaxRetries, err)
			}
			logger.Warn("Ошибка попытки при отправке запроса, будет повтор", zap.Int("attempt", attempt), zap.Error(err))
			time.Sleep(time.Duration(embeddingRetryDelay * float64(time.Second)))
			continue
		}
		defer resp.Body.Close()

		// Читаем тело ответа
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Error("Ошибка чтения данных ответа", zap.Error(err), zap.Int("attempt", attempt))
			if attempt == embeddingMaxRetries {
				return nil, fmt.Errorf("ошибка чтения данных ответа после %d попыток: %v", embeddingMaxRetries, err)
			}
			logger.Warn("Ошибка чтения данных ответа, будет повтор", zap.Int("attempt", attempt), zap.Error(err))
			time.Sleep(time.Duration(embeddingRetryDelay * float64(time.Second)))
			continue
		}

		// Проверяем статус-код ответа
		if resp.StatusCode != http.StatusOK {
			escapedRespBody := html.EscapeString(string(respBody))
			logger.Error("Ошибка API",
				zap.Int("status_code", resp.StatusCode),
				zap.String("response_body", escapedRespBody))
			if attempt == embeddingMaxRetries {
				return nil, fmt.Errorf("ошибка LLM API (%d) после %d попыток: %s", resp.StatusCode, embeddingMaxRetries, escapedRespBody)
			}
			logger.Warn("Ошибка API, будет повтор",
				zap.Int("attempt", attempt),
				zap.Int("status_code", resp.StatusCode))
			time.Sleep(time.Duration(embeddingRetryDelay * float64(time.Second)))
			continue
		}

		// Декодируем ответ
		var embedding []float32
		if useDirectLLM {
			var embResp EmbeddingResponseDirectLLM
			if err := json.NewDecoder(bytes.NewReader(respBody)).Decode(&embResp); err != nil {
				logger.Error("Ошибка декодирования ответа", zap.Error(err), zap.String("response_body", string(respBody)))
				return nil, fmt.Errorf("ошибка декодирования: %w", err)
			}
			if len(embResp.ResultBody.Embeddings) == 0 || len(embResp.ResultBody.Embeddings[0]) == 0 {
				logger.Error("Нет эмбеддингов в ответе", zap.String("response_body", string(respBody)))
				return nil, fmt.Errorf("нет эмбеддингов в ответе")
			}
			if embResp.ResultBody.Model != embeddingModel {
				logger.Error("Некорректная модель в ответе",
					zap.String("actual_model", embResp.ResultBody.Model),
					zap.String("expected_model", embeddingModel))
				return nil, fmt.Errorf("некорректная модель в ответе: %s, ожидается %s", embResp.ResultBody.Model, embeddingModel)
			}
			embedding = embResp.ResultBody.Embeddings[0]
		} else {
			var embResp EmbeddingResponseOpenAI
			if err := json.NewDecoder(bytes.NewReader(respBody)).Decode(&embResp); err != nil {
				logger.Error("Ошибка декодирования ответа", zap.Error(err), zap.String("response_body", string(respBody)))
				return nil, fmt.Errorf("ошибка декодирования: %w", err)
			}
			if len(embResp.Data) == 0 || len(embResp.Data[0].Embedding) == 0 {
				logger.Error("Нет эмбеддингов в ответе", zap.String("response_body", string(respBody)))
				return nil, fmt.Errorf("нет эмбеддингов в ответе")
			}
			if embResp.Model != embeddingModel {
				logger.Error("Некорректная модель в ответе",
					zap.String("actual_model", embResp.Model),
					zap.String("expected_model", embeddingModel))
				return nil, fmt.Errorf("некорректная модель в ответе: %s, ожидается %s", embResp.Model, embeddingModel)
			}
			embedding = embResp.Data[0].Embedding
		}

		// Проверяем размерность эмбеддинга
		if len(embedding) != 1024 {
			logger.Error("Неправильная размерность эмбеддинга",
				zap.Int("actual_size", len(embedding)),
				zap.Int("expected_size", 1024))
			return nil, fmt.Errorf("неожиданная размерность эмбеддинга: %d, ожидается 1024", len(embedding))
		}

		return embedding, nil
	}
	return nil, fmt.Errorf("не удалось выполнить запрос после %d попыток", embeddingMaxRetries)
}
