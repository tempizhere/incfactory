package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
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
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Texts       string `json:"texts"`
		RequestType string `json:"request_type"`
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
	CorrelationID string `json:"correlation_id"`
	DateTime      string `json:"date_time"`
	Mask          bool   `json:"mask"`
	ResultBody    struct {
		Provider   string      `json:"provider"`
		Model      string      `json:"model"`
		ResultType string      `json:"result_type"`
		Error      string      `json:"error,omitempty"`
		Detail     string      `json:"detail,omitempty"`
		Embeddings [][]float32 `json:"embeddings"`
		Usage      struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result_body"`
}

// calculateBackoffDelay вычисляет задержку с экспоненциальным ростом и jitter
func calculateBackoffDelay(baseDelay float64, attempt int) time.Duration {
	// Экспоненциальная задержка: baseDelay * 2^(attempt-1)
	exponentialDelay := baseDelay * math.Pow(2, float64(attempt-1))

	// Добавляем jitter (±25% от задержки)
	jitter := exponentialDelay * 0.25 * (rand.Float64()*2 - 1)

	// Минимальная задержка 1 секунда, максимальная 60 секунд
	finalDelay := exponentialDelay + jitter
	if finalDelay < 1.0 {
		finalDelay = 1.0
	} else if finalDelay > 60.0 {
		finalDelay = 60.0
	}

	return time.Duration(finalDelay * float64(time.Second))
}

// parseRetryAfter парсит заголовок Retry-After
func parseRetryAfter(retryAfter string) (time.Duration, error) {
	if retryAfter == "" {
		return 0, fmt.Errorf("пустой заголовок Retry-After")
	}

	// Пробуем парсить как число секунд
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}

	// Пробуем парсить как HTTP дату
	if t, err := time.Parse(time.RFC1123, retryAfter); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			return delay, nil
		}
		return 0, fmt.Errorf("Retry-After дата в прошлом")
	}

	return 0, fmt.Errorf("не удалось парсить Retry-After: %s", retryAfter)
}

// GenerateEmbedding генерирует эмбеддинг для заданного запроса
func GenerateEmbedding(req types.EmbeddingRequest) ([]float32, error) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Инициализируем генератор случайных чисел для jitter
	rand.Seed(time.Now().UnixNano())

	// Обрезаем текст до максимальной длины
	text := req.Text
	if text == "" {
		logger.Error("Пустой текст для эмбеддинга")
		return nil, fmt.Errorf("пустой текст для эмбеддинга")
	}
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
		// Для Direct-LLM API используем провайдер без суффикса
		provider := embeddingProvider
		if provider == "x5-embed" {
			provider = "x5"
		}
		data := EmbeddingRequestDirectLLM{}
		data.RequestBody.Provider = provider
		data.RequestBody.Model = embeddingModel
		data.RequestBody.Texts = text
		data.RequestBody.RequestType = "embeddings"
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

	// Логируем запрос для отладки
	logger.Info("Отправка запроса эмбеддинга",
		zap.String("url", url),
		zap.String("provider", embeddingProvider),
		zap.String("model", embeddingModel),
		zap.String("use_direct_llm", fmt.Sprintf("%t", useDirectLLM)),
		zap.String("request_body", string(body)))

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
			delay := calculateBackoffDelay(embeddingRetryDelay, attempt)
			logger.Info("Ожидание перед повтором", zap.Duration("delay", delay), zap.Int("attempt", attempt))
			time.Sleep(delay)
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
			delay := calculateBackoffDelay(embeddingRetryDelay, attempt)
			logger.Info("Ожидание перед повтором", zap.Duration("delay", delay), zap.Int("attempt", attempt))
			time.Sleep(delay)
			continue
		}

		// Проверяем статус-код ответа
		if resp.StatusCode != http.StatusOK {
			escapedRespBody := html.EscapeString(string(respBody))
			logger.Error("Ошибка API",
				zap.Int("status_code", resp.StatusCode),
				zap.String("response_body", escapedRespBody),
				zap.String("url", url),
				zap.String("provider", embeddingProvider),
				zap.String("model", embeddingModel))

			// Специальная обработка 429 ошибки (Rate Limit)
			if resp.StatusCode == http.StatusTooManyRequests {
				var delay time.Duration

				// Пытаемся прочитать заголовок Retry-After
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if parsedDelay, err := parseRetryAfter(retryAfter); err == nil {
						delay = parsedDelay
						logger.Info("Получен заголовок Retry-After",
							zap.String("retry_after", retryAfter),
							zap.Duration("parsed_delay", delay))
					} else {
						logger.Warn("Ошибка парсинга Retry-After, используем экспоненциальную задержку",
							zap.String("retry_after", retryAfter),
							zap.Error(err))
						delay = calculateBackoffDelay(embeddingRetryDelay, attempt)
					}
				} else {
					// Если нет Retry-After, используем экспоненциальную задержку
					delay = calculateBackoffDelay(embeddingRetryDelay, attempt)
				}

				if attempt == embeddingMaxRetries {
					logger.Error("Rate limit превышен после всех попыток",
						zap.Int("attempt", attempt),
						zap.Duration("last_delay", delay))
					return nil, fmt.Errorf("rate limit превышен после %d попыток", embeddingMaxRetries)
				}

				logger.Warn("Rate limit превышен, ожидание перед повтором",
					zap.Int("attempt", attempt),
					zap.Duration("delay", delay))
				time.Sleep(delay)
				continue
			}

			// Обработка других ошибок
			if attempt == embeddingMaxRetries {
				return nil, fmt.Errorf("ошибка LLM API (%d) после %d попыток: %s", resp.StatusCode, embeddingMaxRetries, escapedRespBody)
			}
			logger.Warn("Ошибка API, будет повтор",
				zap.Int("attempt", attempt),
				zap.Int("status_code", resp.StatusCode))
			delay := calculateBackoffDelay(embeddingRetryDelay, attempt)
			logger.Info("Ожидание перед повтором", zap.Duration("delay", delay), zap.Int("attempt", attempt))
			time.Sleep(delay)
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

			// Проверяем на ошибки в ответе
			if embResp.ResultBody.ResultType == "error" {
				logger.Error("API вернул ошибку в ответе",
					zap.String("correlation_id", embResp.CorrelationID),
					zap.String("error", embResp.ResultBody.Error),
					zap.String("detail", embResp.ResultBody.Detail),
					zap.String("provider", embeddingProvider),
					zap.String("model", embeddingModel))
				return nil, fmt.Errorf("Direct-LLM error (corr_id=%s, error=%s): %s", embResp.CorrelationID, embResp.ResultBody.Error, embResp.ResultBody.Detail)
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

		logger.Info("Эмбеддинг успешно сгенерирован",
			zap.Int("embedding_size", len(embedding)),
			zap.String("model", embeddingModel))

		return embedding, nil
	}
	return nil, fmt.Errorf("не удалось выполнить запрос после %d попыток", embeddingMaxRetries)
}
