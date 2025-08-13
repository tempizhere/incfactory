package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tempizhere/incfactory/internal/llm"
	"github.com/tempizhere/incfactory/internal/queue"
	"github.com/tempizhere/incfactory/internal/types"

	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/time/rate"
)

type Config struct {
	RabbitMQHost           string
	RabbitMQPort           string
	RabbitMQUser           string
	RabbitMQPass           string
	EmbeddingHost          string
	EmbeddingAPIKey        string
	EmbeddingProvider      string
	EmbeddingModel         string
	CompletionHost         string
	CompletionAPIKey       string
	CompletionProvider     string
	CompletionModel        string
	EmbeddingRateLimit     float64
	LLMRateLimit           float64
	LLMMaxRetries          int
	EmbeddingMaxRetries    int
	LLMRetryDelay          float64
	EmbeddingRetryDelay    float64
	EmbeddingMaxTextLength int
	LLMMaxTokens           int
	LLMJsonParseRetries    int
}

type PromptConfig struct {
	Prompt           string  `json:"prompt"`
	Temperature      float32 `json:"temperature,omitempty"`
	TopP             float32 `json:"top_p,omitempty"`
	FrequencyPenalty float32 `json:"frequency_penalty,omitempty"`
	PresencePenalty  float32 `json:"presence_penalty,omitempty"`
}

func main() {
	if os.Getenv("RABBITMQ_HOST") == "" || os.Getenv("EMBEDDING_HOST") == "" || os.Getenv("LLM_HOST") == "" {
		if err := godotenv.Load(); err != nil {
			fmt.Println("Ошибка загрузки .env:", err)
			return
		}
	}

	config := Config{
		RabbitMQHost:       os.Getenv("RABBITMQ_HOST"),
		RabbitMQPort:       os.Getenv("RABBITMQ_PORT"),
		RabbitMQUser:       os.Getenv("RABBITMQ_USER"),
		RabbitMQPass:       os.Getenv("RABBITMQ_PASS"),
		EmbeddingHost:      os.Getenv("EMBEDDING_HOST"),
		EmbeddingAPIKey:    os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingProvider:  os.Getenv("EMBEDDING_PROVIDER"),
		EmbeddingModel:     os.Getenv("EMBEDDING_MODEL"),
		CompletionHost:     os.Getenv("LLM_HOST"),
		CompletionAPIKey:   os.Getenv("LLM_API_KEY"),
		CompletionProvider: os.Getenv("LLM_PROVIDER"),
		CompletionModel:    os.Getenv("LLM_MODEL"),
	}

	if err := validateConfig(config); err != nil {
		fmt.Printf("Ошибка валидации конфигурации: %v\n", err)
		return
	}

	var err error
	config.EmbeddingRateLimit, err = parseFloatEnv("EMBEDDING_RATE_LIMIT")
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.LLMRateLimit, err = parseFloatEnv("LLM_RATE_LIMIT")
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.LLMMaxRetries, err = parseIntEnv("LLM_MAX_RETRIES", 3)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.EmbeddingMaxRetries, err = parseIntEnv("EMBEDDING_MAX_RETRIES", 3)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.LLMRetryDelay, err = parseFloatEnvWithDefault("LLM_RETRY_DELAY", 1.0)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.EmbeddingRetryDelay, err = parseFloatEnvWithDefault("EMBEDDING_RETRY_DELAY", 1.0)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.EmbeddingMaxTextLength, err = parseIntEnv("EMBEDDING_MAX_TEXT_LENGTH", 1000)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.LLMMaxTokens, err = parseIntEnv("LLM_MAX_TOKENS", 10000)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}
	config.LLMJsonParseRetries, err = parseIntEnv("LLM_JSON_PARSE_RETRIES", 3)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return
	}

	if err := queue.Init(config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser, config.RabbitMQPass); err != nil {
		fmt.Printf("Ошибка инициализации RabbitMQ: %v\n", err)
		return
	}
	defer queue.Close()

	llm.Init(config.EmbeddingHost, config.EmbeddingAPIKey, config.EmbeddingProvider, config.EmbeddingModel,
		config.CompletionHost, config.CompletionAPIKey, config.CompletionProvider, config.CompletionModel,
		config.EmbeddingMaxRetries, config.EmbeddingRetryDelay, config.EmbeddingMaxTextLength)

	embeddingLimiter := rate.NewLimiter(rate.Limit(config.EmbeddingRateLimit), 1)
	llmLimiter := rate.NewLimiter(rate.Limit(config.LLMRateLimit), 1)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		msgs, err := queue.ConsumeLLMTasks()
		if err != nil {
			fmt.Printf("Ошибка регистрации потребителя llm_tasks: %v\n", err)
			return
		}

		semaphore := make(chan struct{}, 10)
		for msg := range msgs {
			semaphore <- struct{}{}
			go func(d amqp.Delivery) {
				defer func() { <-semaphore }()

				var task types.LLMTask
				if err := json.Unmarshal(d.Body, &task); err != nil {
					fmt.Printf("Ошибка десериализации LLM-запроса: %v\n", err)
					d.Nack(false, false)
					return
				}

				result := types.LLMResult{
					RequestID: task.RequestID,
					Source:    task.Source,
					Type:      task.Type,
				}

				var payloadBytes []byte
				switch p := task.Payload.(type) {
				case json.RawMessage:
					payloadBytes = p
				default:
					var err error
					payloadBytes, err = json.Marshal(p)
					if err != nil {
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка сериализации Payload: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}
				}

				switch task.Type {
				case "summary", "recommendation":
					var req types.LLMRequest
					if err := json.Unmarshal(payloadBytes, &req); err != nil {
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка десериализации запроса: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					if err := llmLimiter.Wait(context.Background()); err != nil {
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка LLM rate limiter: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					promptFile, err := os.ReadFile("config/summary_prompt.json")
					if task.Type == "recommendation" {
						promptFile, err = os.ReadFile("config/assistant_prompt.json")
					}
					if err != nil {
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка чтения промпта: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}
					var promptConfig PromptConfig
					if err := json.Unmarshal(promptFile, &promptConfig); err != nil {
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка парсинга промпта: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					temperature := float64(promptConfig.Temperature)
					if temperature == 0 {
						temperature = 0.7
					}
					var response string
					for parseAttempt := 1; parseAttempt <= config.LLMJsonParseRetries; parseAttempt++ {
						for attempt := 1; attempt <= config.LLMMaxRetries; attempt++ {
							var err error
							response, err = llm.GenerateCompletion(req.Messages, temperature, config.LLMMaxTokens,
								promptConfig.TopP, promptConfig.FrequencyPenalty, promptConfig.PresencePenalty)
							if err == nil {
								break
							}
							fmt.Printf("Ошибка генерации для request_id %s (попытка %d/%d): %v\n", task.RequestID, attempt, config.LLMMaxRetries, err)
							if attempt < config.LLMMaxRetries {
								time.Sleep(time.Duration(config.LLMRetryDelay*math.Pow(2, float64(attempt))) * time.Second)
							} else {
								result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Ошибка генерации после %d попыток: %v", config.LLMMaxRetries, err)})
								if err := queue.PublishLLMResult(result); err != nil {
									fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
								}
								d.Nack(false, false)
								return
							}
						}

						response = cleanLLMResponse(response)
						re := regexp.MustCompile(`\[\{"summary":"((?:[^"\\]|\\(?:[^u]|u[0-9a-fA-F]{4})|\\")*)","solution":"((?:[^"\\]|\\(?:[^u]|u[0-9a-fA-F]{4})|\\")*)","category":"((?:[^"\\]|\\(?:[^u]|u[0-9a-fA-F]{4})|\\")*)"\}\]`)
						if task.Type == "recommendation" {
							re = regexp.MustCompile(`(?s)\[\{"similar_cards":\[(.*?)\],"recommended_solution":"((?:[^"\\]|\\(?:[^u]|u[0-9a-fA-F]{4})|\\")*)"\}\]`)
						}
						matches := re.FindStringSubmatch(response)
						if task.Type == "summary" && len(matches) == 4 {
							resp := types.LLMResponse{
								CardID:   req.CardID,
								Summary:  matches[1],
								Solution: matches[2],
								Category: matches[3],
							}
							if resp.Summary == "" || resp.Solution == "" || resp.Category == "" {
								fmt.Printf("Пустой ответ LLM для request_id %s\n", task.RequestID)
								if parseAttempt < config.LLMJsonParseRetries {
									temperature = math.Max(0.1, temperature-0.2)
									fmt.Printf("Повтор запроса для request_id %s с temperature=%.2f (попытка парсинга %d/%d)\n", task.RequestID, temperature, parseAttempt+1, config.LLMJsonParseRetries)
									continue
								}
								result.Payload, _ = json.Marshal(types.LLMResponse{Error: "Пустой ответ LLM"})
							} else {
								result.Payload, _ = json.Marshal(resp)
							}
							break
						} else if task.Type == "recommendation" && len(matches) == 3 {
							resp := map[string]interface{}{
								"similar_cards":        matches[1],
								"recommended_solution": matches[2],
							}
							result.Payload, _ = json.Marshal(resp)
							break
						}

						var summaryResp []map[string]interface{}
						response = strings.TrimPrefix(response, "\uFEFF")
						if err := json.Unmarshal([]byte(response), &summaryResp); err == nil && len(summaryResp) > 0 {
							result.Payload, _ = json.Marshal(summaryResp[0])
							break
						}
						if parseAttempt < config.LLMJsonParseRetries {
							temperature = math.Max(0.1, temperature-0.2)
							fmt.Printf("Повтор запроса для request_id %s с temperature=%.2f (попытка парсинга %d/%d)\n", task.RequestID, temperature, parseAttempt+1, config.LLMJsonParseRetries)
							continue
						}
						result.Payload, _ = json.Marshal(types.LLMResponse{Error: "Некорректный формат JSON в ответе"})
					}

				case "embedding":
					var req types.EmbeddingRequest
					if err := json.Unmarshal(payloadBytes, &req); err != nil {
						result.Payload, _ = json.Marshal(types.EmbeddingResponse{Error: fmt.Sprintf("Ошибка десериализации запроса: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					if err := embeddingLimiter.Wait(context.Background()); err != nil {
						result.Payload, _ = json.Marshal(types.EmbeddingResponse{Error: fmt.Sprintf("Ошибка embedding rate limiter: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					embedding, err := llm.GenerateEmbedding(req)
					if err != nil {
						fmt.Printf("Ошибка генерации эмбеддинга для %s %s: %v\n", req.SourceType, req.SourceID, err)
						result.Payload, _ = json.Marshal(types.EmbeddingResponse{Error: fmt.Sprintf("Ошибка генерации эмбеддинга: %v", err)})
						if err := queue.PublishLLMResult(result); err != nil {
							fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
						}
						d.Nack(false, false)
						return
					}

					resp := types.EmbeddingResponse{
						SourceID:   req.SourceID,
						SourceType: req.SourceType,
						Text:       req.Text,
						Embedding:  embedding,
					}
					result.Payload, _ = json.Marshal(resp)

				default:
					result.Payload, _ = json.Marshal(types.LLMResponse{Error: fmt.Sprintf("Недопустимый тип задачи: %s", task.Type)})
					if err := queue.PublishLLMResult(result); err != nil {
						fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
					}
					d.Nack(false, false)
					return
				}

				if err := queue.PublishLLMResult(result); err != nil {
					fmt.Printf("Ошибка отправки ответа для request_id %s: %v\n", task.RequestID, err)
					d.Nack(false, false)
					return
				}

				d.Ack(false)
			}(msg)
		}
	}()

	wg.Wait()
}

func validateConfig(config Config) error {
	required := []struct {
		name  string
		value string
	}{
		{"RABBITMQ_HOST", config.RabbitMQHost},
		{"RABBITMQ_PORT", config.RabbitMQPort},
		{"RABBITMQ_USER", config.RabbitMQUser},
		{"RABBITMQ_PASS", config.RabbitMQPass},
		{"EMBEDDING_HOST", config.EmbeddingHost},
		{"EMBEDDING_API_KEY", config.EmbeddingAPIKey},
		{"EMBEDDING_PROVIDER", config.EmbeddingProvider},
		{"EMBEDDING_MODEL", config.EmbeddingModel},
		{"LLM_HOST", config.CompletionHost},
		{"LLM_API_KEY", config.CompletionAPIKey},
		{"LLM_PROVIDER", config.CompletionProvider},
		{"LLM_MODEL", config.CompletionModel},
	}
	for _, env := range required {
		if env.value == "" {
			return fmt.Errorf("переменная %s не установлена", env.name)
		}
	}
	return nil
}

func parseFloatEnv(key string) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return 0, fmt.Errorf("%s не установлена", key)
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s должен быть положительным числом, получено: %s", key, value)
	}
	return f, nil
}

func parseFloatEnvWithDefault(key string, defaultValue float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s должен быть положительным числом, получено: %s", key, value)
	}
	return f, nil
}

func parseIntEnv(key string, defaultValue int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	i, err := strconv.Atoi(value)
	if err != nil || i <= 0 {
		return 0, fmt.Errorf("%s должен быть положительным целым числом, получено: %s", key, value)
	}
	return i, nil
}

func cleanLLMResponse(response string) string {
	if idx := strings.LastIndex(response, "]"); idx != -1 {
		return response[:idx+1]
	}
	return response
}
