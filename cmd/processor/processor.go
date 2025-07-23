package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/tempizhere/incfactory/internal/api"
	"github.com/tempizhere/incfactory/internal/db"
	"github.com/tempizhere/incfactory/internal/queue"
	"github.com/tempizhere/incfactory/internal/types"
)

type PromptConfig struct {
	Prompt           string  `json:"prompt"`
	Temperature      float32 `json:"temperature,omitempty"`
	TopP             float32 `json:"top_p,omitempty"`
	FrequencyPenalty float32 `json:"frequency_penalty,omitempty"`
	PresencePenalty  float32 `json:"presence_penalty,omitempty"`
}

// cleanText очищает текст от Markdown, экранированных символов и нормализует его
func cleanText(text string) string {
	text = strings.ReplaceAll(text, `\u003e`, ">")
	text = strings.ReplaceAll(text, `\"`, "'")
	reInvalid := regexp.MustCompile(`\\u[0-9a-fA-F]{0,3}(?:[^0-9a-fA-F]|$)|\\[^u"\\]`)
	text = reInvalid.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "---", "")
	text = strings.ReplaceAll(text, "##", "")
	text = strings.ReplaceAll(text, "###", "")
	reQuote := regexp.MustCompile(`(?m)^>\s*`)
	text = reQuote.ReplaceAllString(text, "")
	reLink := regexp.MustCompile(`\[(.*?)\]\((https?://[^\s)]+)\)`)
	text = reLink.ReplaceAllString(text, "$1")
	reList := regexp.MustCompile(`(?m)^\s*[-*]\s+`)
	text = reList.ReplaceAllString(text, ", ")
	text = strings.ReplaceAll(text, "”", "\"")
	text = strings.ReplaceAll(text, "“", "\"")
	reEmptySection := regexp.MustCompile(`(?i)(Примеры|Лог\(-и\)):\s*`)
	text = reEmptySection.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\n", "; ")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\t", " ")
	reSpaces := regexp.MustCompile(`\s+`)
	text = reSpaces.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// handleError унифицирует обработку ошибок с логированием и Nack
func handleError(d *amqp.Delivery, err error, contextID string, message string) {
	fmt.Printf("%s: %v (message: %s)\n", contextID, err, string(d.Body))
	d.Nack(false, false)
}

// buildLLMRequest формирует сообщения для LLM-запроса
func buildLLMRequest(card *api.Card, comments []api.Comment, promptConfig *PromptConfig) *types.LLMTask {
	var commentTitles []string
	for _, comment := range comments {
		if comment.Text != "" && strings.TrimSpace(comment.Text) != "" {
			commentTitles = append(commentTitles, cleanText(comment.Text))
		}
	}

	var description string
	if card.Description != "" {
		description = cleanText(card.Description)
	}
	contextStr := fmt.Sprintf("Title: %s\nDescription: %s\nComments:\n%s",
		cleanText(card.Title), description, strings.Join(commentTitles, "\n"))
	prompt := strings.Replace(promptConfig.Prompt, "{context}", contextStr, -1)
	messages := []types.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: contextStr},
	}

	return &types.LLMTask{
		RequestID: fmt.Sprintf("proc-%d", card.ID),
		Source:    "processor",
		Type:      "summary",
		Payload: types.LLMRequest{
			CardID:   fmt.Sprintf("%d", card.ID),
			Messages: messages,
		},
	}
}

// processCardTransaction обрабатывает карточку и комментарии в транзакции
func processCardTransaction(d *amqp.Delivery, cardMsg queue.CardWithComments) error {
	exists, updatedChanged, err := db.CheckCardExists(cardMsg.Card)
	if err != nil {
		return fmt.Errorf("ошибка проверки карточки %d: %w", cardMsg.Card.ID, err)
	}
	if exists && !updatedChanged {
		summaryExists, err := db.CheckSummaryExists(fmt.Sprintf("%d", cardMsg.Card.ID))
		if err != nil {
			return fmt.Errorf("ошибка проверки суммарии для карточки %d: %w", cardMsg.Card.ID, err)
		}
		if summaryExists {
			return nil
		}
	}

	if cardMsg.Card.Title != "" {
		cardMsg.Card.Title = cleanText(cardMsg.Card.Title)
	}
	if cardMsg.Card.Description != "" {
		cardMsg.Card.Description = cleanText(cardMsg.Card.Description)
	}

	tx, err := db.DB().Begin()
	if err != nil {
		return fmt.Errorf("ошибка начала транзакции для карточки %d: %w", cardMsg.Card.ID, err)
	}
	defer tx.Rollback()

	if err := db.SaveCard(cardMsg.Card); err != nil {
		return fmt.Errorf("ошибка сохранения карточки %d: %w", cardMsg.Card.ID, err)
	}

	for _, comment := range cardMsg.Comments {
		commentExists, commentUpdatedChanged, err := db.CheckCommentExists(comment, cardMsg.Card.ID)
		if err != nil {
			return fmt.Errorf("ошибка проверки комментария %d: %w", comment.ID, err)
		}
		if commentExists && !commentUpdatedChanged {
			continue
		}

		if comment.Text != "" {
			comment.Text = cleanText(comment.Text)
		}

		if err := db.SaveComment(comment, cardMsg.Card.ID); err != nil {
			return fmt.Errorf("ошибка сохранения комментария %d: %w", comment.ID, err)
		}
	}

	summaryExists, err := db.CheckSummaryExists(fmt.Sprintf("%d", cardMsg.Card.ID))
	if err != nil {
		return fmt.Errorf("ошибка проверки суммарии для карточки %d: %w", cardMsg.Card.ID, err)
	}
	if summaryExists {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("ошибка коммита транзакции для карточки %d: %w", cardMsg.Card.ID, err)
		}
		fmt.Printf("Карточка %d сохранена\n", cardMsg.Card.ID)
		return nil
	}

	comments, err := db.GetCommentsByCardID(fmt.Sprintf("%d", cardMsg.Card.ID))
	if err != nil {
		return fmt.Errorf("ошибка получения комментариев для карточки %d: %w", cardMsg.Card.ID, err)
	}

	promptFile, err := os.ReadFile("config/summary_prompt.json")
	if err != nil {
		return fmt.Errorf("ошибка чтения config/summary_prompt.json: %v", err)
	}
	var promptConfig PromptConfig
	if err := json.Unmarshal(promptFile, &promptConfig); err != nil {
		return fmt.Errorf("ошибка парсинга config/summary_prompt.json: %v", err)
	}

	llmTask := buildLLMRequest(&cardMsg.Card, comments, &promptConfig)
	if err := queue.PublishLLMTask(*llmTask); err != nil {
		return fmt.Errorf("ошибка отправки запроса на суммаризацию для карточки %d: %w", cardMsg.Card.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ошибка коммита транзакции для карточки %d: %w", cardMsg.Card.ID, err)
	}
	fmt.Printf("Карточка %d сохранена\n", cardMsg.Card.ID)

	return nil
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("Ошибка загрузки .env:", err)
		return
	}

	config := struct {
		RabbitMQHost string
		RabbitMQPort string
		RabbitMQUser string
		RabbitMQPass string
		DBHost       string
		DBPort       string
		DBUser       string
		DBPassword   string
		DBName       string
	}{
		RabbitMQHost: os.Getenv("RABBITMQ_HOST"),
		RabbitMQPort: os.Getenv("RABBITMQ_PORT"),
		RabbitMQUser: os.Getenv("RABBITMQ_USER"),
		RabbitMQPass: os.Getenv("RABBITMQ_PASS"),
		DBHost:       os.Getenv("DB_HOST"),
		DBPort:       os.Getenv("DB_PORT"),
		DBUser:       os.Getenv("DB_USER"),
		DBPassword:   os.Getenv("DB_PASSWORD"),
		DBName:       os.Getenv("DB_NAME"),
	}

	required := []string{
		"RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASS",
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
	}
	for _, env := range required {
		if os.Getenv(env) == "" {
			fmt.Printf("Ошибка: Переменная %s не установлена\n", env)
			return
		}
	}

	if err := queue.Init(config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser, config.RabbitMQPass); err != nil {
		fmt.Printf("Ошибка инициализации RabbitMQ: %v\n", err)
		return
	}
	defer queue.Close()

	if err := db.Init(config.DBHost, config.DBPort, config.DBUser, config.DBPassword, config.DBName); err != nil {
		fmt.Println("Ошибка инициализации PostgreSQL:", err)
		return
	}
	defer db.Close()

	var wg sync.WaitGroup

	// Обработка карточек и комментариев
	wg.Add(1)
	go func() {
		defer wg.Done()
		msgs, err := queue.ConsumeCardWithComments()
		if err != nil {
			fmt.Printf("Ошибка регистрации потребителя kaiten_transactions: %v\n", err)
			return
		}

		semaphore := make(chan struct{}, 50)
		for msg := range msgs {
			semaphore <- struct{}{}
			go func(d amqp.Delivery) {
				defer func() { <-semaphore }()

				var cardMsg queue.CardWithComments
				if err := json.Unmarshal(d.Body, &cardMsg); err != nil {
					handleError(&d, err, fmt.Sprintf("Ошибка десериализации сообщения для карточки %d", cardMsg.Card.ID), "ошибка десериализации сообщения")
					return
				}

				if err := processCardTransaction(&d, cardMsg); err != nil {
					handleError(&d, err, fmt.Sprintf("Ошибка обработки карточки %d", cardMsg.Card.ID), err.Error())
					return
				}

				d.Ack(false)
			}(msg)
		}
	}()

	// Обработка результатов
	wg.Add(1)
	go func() {
		defer wg.Done()
		msgs, err := queue.ConsumeLLMResults()
		if err != nil {
			fmt.Printf("Ошибка регистрации потребителя llm_results: %v\n", err)
			return
		}

		semaphore := make(chan struct{}, 50)
		for msg := range msgs {
			semaphore <- struct{}{}
			go func(d amqp.Delivery) {
				defer func() { <-semaphore }()

				var llmResult types.LLMResult
				if err := json.Unmarshal(d.Body, &llmResult); err != nil {
					handleError(&d, err, "Ошибка десериализации результата LLM", "ошибка десериализации")
					return
				}

				if llmResult.Source != "processor" {
					d.Ack(false)
					return
				}

				switch llmResult.Type {
				case "summary":
					var resp types.LLMResponse
					if err := json.Unmarshal(llmResult.Payload, &resp); err != nil {
						handleError(&d, err, fmt.Sprintf("Ошибка десериализации ответа LLM для карточки %s", resp.CardID), "ошибка десериализации ответа LLM")
						return
					}

					if resp.Error != "" {
						fmt.Printf("Ошибка обработки LLM-запроса для card_id %s: %s\n", resp.CardID, resp.Error)
						d.Nack(false, false)
						return
					}

					if err := db.SaveCardSummary(resp.CardID, resp.Summary, resp.Solution, resp.Category); err != nil {
						handleError(&d, err, fmt.Sprintf("Ошибка сохранения суммарии для карточки %s", resp.CardID), "ошибка сохранения суммарии")
						return
					}
					fmt.Printf("Карточка %s обработана LLM и сохранена\n", resp.CardID)

					if resp.Summary != "" {
						embTask := types.LLMTask{
							RequestID: fmt.Sprintf("proc-emb-summary-%s", resp.CardID),
							Source:    "processor",
							Type:      "embedding",
							Payload: types.EmbeddingRequest{
								SourceID:   fmt.Sprintf("%s_summary", resp.CardID),
								SourceType: "summary",
								Text:       cleanText(resp.Summary),
							},
						}
						if err := queue.PublishLLMTask(embTask); err != nil {
							handleError(&d, err, fmt.Sprintf("Ошибка отправки запроса на эмбеддинг summary для карточки %s", resp.CardID), "ошибка отправки запроса на эмбеддинг")
							return
						}
					}

					if resp.Solution != "" {
						embTask := types.LLMTask{
							RequestID: fmt.Sprintf("proc-emb-solution-%s", resp.CardID),
							Source:    "processor",
							Type:      "embedding",
							Payload: types.EmbeddingRequest{
								SourceID:   fmt.Sprintf("%s_solution", resp.CardID),
								SourceType: "solution",
								Text:       cleanText(resp.Solution),
							},
						}
						if err := queue.PublishLLMTask(embTask); err != nil {
							handleError(&d, err, fmt.Sprintf("Ошибка отправки запроса на эмбеддинг solution для карточки %s", resp.CardID), "ошибка отправки запроса на эмбеддинг")
							return
						}
					}

				case "embedding":
					var resp types.EmbeddingResponse
					if err := json.Unmarshal(llmResult.Payload, &resp); err != nil {
						handleError(&d, err, fmt.Sprintf("Ошибка десериализации ответа эмбеддинга для %s %s", resp.SourceType, resp.SourceID), "ошибка десериализации ответа эмбеддинга")
						return
					}

					if resp.Error != "" {
						fmt.Printf("Ошибка обработки запроса на эмбеддинг %s %s: %s\n", resp.SourceType, resp.SourceID, resp.Error)
						d.Nack(false, false)
						return
					}

					switch resp.SourceType {
					case "summary":
						if err := db.SaveSummaryEmbedding(resp.SourceID, "summary", resp.Embedding, resp.Text); err != nil {
							handleError(&d, err, fmt.Sprintf("Ошибка сохранения эмбеддинга summary для %s", resp.SourceID), "ошибка сохранения эмбеддинга")
							return
						}
					case "solution":
						if err := db.SaveSummaryEmbedding(resp.SourceID, "solution", resp.Embedding, resp.Text); err != nil {
							handleError(&d, err, fmt.Sprintf("Ошибка сохранения эмбеддинга solution для %s", resp.SourceID), "ошибка сохранения эмбеддинга")
							return
						}
					default:
						fmt.Printf("Недопустимый source_type: %s\n", resp.SourceType)
						d.Nack(false, false)
						return
					}

				default:
					fmt.Printf("Недопустимый тип результата: %s\n", llmResult.Type)
					d.Nack(false, false)
					return
				}

				d.Ack(false)
			}(msg)
		}
	}()

	wg.Wait()
}
