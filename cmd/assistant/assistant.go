package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/tempizhere/incfactory/internal/db"
	"github.com/tempizhere/incfactory/internal/queue"
	"github.com/tempizhere/incfactory/internal/types"
)

// НОВАЯ АРХИТЕКТУРА: Stateless без in-memory каналов
type AssistantService struct {
	mu sync.RWMutex
	// Каналы для ожидания результатов по correlation_id
	resultChans map[string]chan *types.LLMResult
	resultMu    sync.RWMutex
}

var assistant = &AssistantService{
	resultChans: make(map[string]chan *types.LLMResult),
}

// НОВАЯ ЛОГИКА: Простая функция для ожидания результата
func (a *AssistantService) waitForLLMResult(ctx context.Context, correlationID string, timeout time.Duration) (*types.LLMResult, error) {
	// Создаем канал для ожидания результата
	resultChan := make(chan *types.LLMResult, 1)

	// Регистрируем канал для этого correlation_id
	a.resultMu.Lock()
	a.resultChans[correlationID] = resultChan
	a.resultMu.Unlock()

	// Очищаем канал при выходе
	defer func() {
		a.resultMu.Lock()
		delete(a.resultChans, correlationID)
		a.resultMu.Unlock()
	}()

	// Ожидаем результат с таймаутом
	select {
	case result := <-resultChan:
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout ожидания результата для correlation_id: %s", correlationID)
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout ожидания результата для correlation_id: %s", correlationID)
	}
}

// НОВАЯ ЛОГИКА: Упрощенная обработка нового запроса
func (a *AssistantService) processNewCard(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("🚀 Начало обработки нового запроса\n")

	// Поддерживаем старый формат карточки
	var card struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		BoardID     int    `json:"board_id"`
		ColumnID    int    `json:"column_id"`
		SpaceID     string `json:"space_id"`
		Parents     []struct {
			ID int `json:"id"`
		} `json:"parents"`
		Created string `json:"created"`
	}

	if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
		http.Error(w, "Ошибка парсинга JSON", http.StatusBadRequest)
		return
	}

	if card.Description == "" {
		http.Error(w, "Описание карточки не может быть пустым", http.StatusBadRequest)
		return
	}

	fmt.Printf("📝 Получена карточка: ID=%d, Title=%s, Description=%s\n", card.ID, card.Title, card.Description)

	// Генерируем correlation ID для отслеживания
	correlationID := fmt.Sprintf("assist-%d", time.Now().UnixNano())
	fmt.Printf("🆔 Создан correlation_id: %s\n", correlationID)

	// 1. Генерируем эмбеддинг
	fmt.Printf("🔍 Генерация эмбеддинга для запроса\n")
	embTask := types.LLMTask{
		RequestID:     fmt.Sprintf("assist-emb-%s", correlationID),
		CorrelationID: correlationID,
		Source:        "assistant",
		Type:          "embedding",
		Payload: types.EmbeddingRequest{
			SourceID:   "query",
			SourceType: "query",
			Text:       card.Description,
		},
	}

	fmt.Printf("📤 Отправка embTask: %s\n", embTask.RequestID)
	if err := queue.PublishLLMTask(embTask); err != nil {
		fmt.Printf("❌ Ошибка отправки задачи эмбеддинга: %v\n", err)
		http.Error(w, "Ошибка отправки задачи", http.StatusInternalServerError)
		return
	}
	fmt.Printf("✅ Задача эмбеддинга отправлена, request_id: %s\n", embTask.RequestID)

	// 2. Ожидаем эмбеддинг
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	embResult, err := a.waitForLLMResult(ctx, correlationID, 60*time.Second)
	if err != nil {
		fmt.Printf("❌ Ошибка получения эмбеддинга: %v\n", err)
		http.Error(w, "Ошибка получения эмбеддинга", http.StatusInternalServerError)
		return
	}

	fmt.Printf("✅ Получен эмбеддинг: %s\n", embResult.RequestID)

	// 3. Десериализуем эмбеддинг
	var embResponse types.EmbeddingResponse
	if err := json.Unmarshal(embResult.Payload, &embResponse); err != nil {
		fmt.Printf("❌ Ошибка десериализации эмбеддинга: %v\n", err)
		http.Error(w, "Ошибка обработки эмбеддинга", http.StatusInternalServerError)
		return
	}

	if embResponse.Error != "" {
		fmt.Printf("❌ Ошибка в эмбеддинге: %s\n", embResponse.Error)
		http.Error(w, "Ошибка генерации эмбеддинга", http.StatusInternalServerError)
		return
	}

	// 4. Ищем похожие карточки
	fmt.Printf("🔍 Поиск похожих карточек\n")
	similarCards, err := db.FindSimilarCards(embResponse.Embedding, 5)
	if err != nil {
		fmt.Printf("❌ Ошибка поиска похожих карточек: %v\n", err)
		http.Error(w, "Ошибка поиска похожих карточек", http.StatusInternalServerError)
		return
	}

	fmt.Printf("✅ Найдено %d похожих карточек\n", len(similarCards))

	// 5. Формируем контекст для LLM
	contextStr := ""
	for _, card := range similarCards {
		contextStr += fmt.Sprintf("Card ID: %s\nSummary: %s\nSolution: %s\n\n",
			card.ID, card.Summary, card.Solution)
	}

	// 6. Генерируем рекомендацию
	fmt.Printf("🤖 Генерация рекомендации\n")
	recTask := types.LLMTask{
		RequestID:     fmt.Sprintf("assist-rec-%s", correlationID),
		CorrelationID: correlationID,
		Source:        "assistant",
		Type:          "recommendation",
		Payload: types.LLMRequest{
			CardID: "assistant_query",
			Messages: []types.Message{
				{
					Role:    "system",
					Content: fmt.Sprintf("Ты — помощник для инженеров. Для карточки '%s' и контекста (%s) верни JSON в формате: [{ \"similar_cards\": [{\"card_id\": string, \"similarity\": string}, ...], \"recommended_solution\": \"строка до 500 символов\" }]. Правила: \n- similar_cards: до 5 объектов с card_id из контекста и их similarity (в формате '90%%'), упорядоченных по убыванию релевантности (наиболее соответствующая запросу карточка первая). \n- recommended_solution: извлеки технические решения (SQL-запросы, команды или действия, такие как 'поменять статус', 'установить через БД', 'снять блокировку', 'проставить через БД') из комментариев топ-5 похожих карточек. Нормализуй текст решений (удали лишние пробелы, переносы строк). Сравни решения по точному совпадению. Если ≥3 карточек имеют одинаковое решение, выбери его. Иначе выбери решение из карточки с наивысшей similarity. Игнорируй комментарии с только ссылками (URL), статусами без инструкций ('решается на L3', 'скорректировано', 'чек создан', 'чек добавлен', 'задача отменена', 'выполнено по запросу'), неинформативными записями ('пример для тс3') или описаниями без действий. Укажи card_id в скобках. Если решения нет, верни: \"Решение не найдено.\" \n- Ответ — строго валидный JSON, только указанная структура, без ```json, ```, без лишних символов, пробелов или переносов строк вне JSON.", card.Description, contextStr),
				},
				{
					Role:    "user",
					Content: card.Description,
				},
			},
		},
	}

	fmt.Printf("📤 Отправка recTask: %s\n", recTask.RequestID)
	if err := queue.PublishLLMTask(recTask); err != nil {
		fmt.Printf("❌ Ошибка отправки задачи рекомендации: %v\n", err)
		http.Error(w, "Ошибка отправки задачи", http.StatusInternalServerError)
		return
	}
	fmt.Printf("✅ Задача рекомендации отправлена, request_id: %s\n", recTask.RequestID)

	// 7. Ожидаем рекомендацию
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel2()

	recResult, err := a.waitForLLMResult(ctx2, correlationID, 120*time.Second)
	if err != nil {
		fmt.Printf("❌ Ошибка получения рекомендации: %v\n", err)
		http.Error(w, "Ошибка получения рекомендации", http.StatusInternalServerError)
		return
	}

	fmt.Printf("✅ Получена рекомендация: %s\n", recResult.RequestID)

	// 8. Отправляем ответ
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := map[string]interface{}{
		"correlation_id": correlationID,
		"request_id":     recResult.RequestID,
		"result":         json.RawMessage(recResult.Payload),
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		fmt.Printf("❌ Ошибка кодирования ответа: %v\n", err)
		http.Error(w, "Ошибка кодирования ответа", http.StatusInternalServerError)
		return
	}

	fmt.Printf("✅ Ответ отправлен для correlation_id: %s\n", correlationID)
}

// startConsumer запускает постоянный consumer для обработки результатов
func (a *AssistantService) startConsumer() {
	go func() {
		for {
			// Подключаемся к очереди результатов
			msgs, err := queue.ConsumeLLMResults()
			if err != nil {
				fmt.Printf("❌ Ошибка подключения к llm_results: %v\n", err)
				time.Sleep(5 * time.Second) // Ждем перед повторной попыткой
				continue
			}

			fmt.Printf("🔄 Начинаем обработку сообщений из llm_results_assistant\n")

			// Обрабатываем сообщения
			for msg := range msgs {
				var llmResult types.LLMResult
				if err := json.Unmarshal(msg.Body, &llmResult); err != nil {
					fmt.Printf("❌ Ошибка десериализации результата: %v\n", err)
					msg.Ack(false)
					continue
				}

				fmt.Printf("📨 Получен результат: correlation_id=%s, request_id=%s, type=%s\n",
					llmResult.CorrelationID, llmResult.RequestID, llmResult.Type)

				// Ищем канал для этого correlation_id
				a.resultMu.RLock()
				resultChan, exists := a.resultChans[llmResult.CorrelationID]
				a.resultMu.RUnlock()

				if exists {
					fmt.Printf("✅ Найден канал для correlation_id: %s\n", llmResult.CorrelationID)
					msg.Ack(false)
					resultChan <- &llmResult
				} else {
					fmt.Printf("⚠️ Канал не найден для correlation_id: %s\n", llmResult.CorrelationID)
					msg.Ack(false)
				}
			}

			fmt.Printf("🔄 Переподключение к очереди llm_results_assistant\n")
		}
	}()
}

func main() {
	// Загружаем .env только если основные переменные не установлены
	if os.Getenv("RABBITMQ_HOST") == "" || os.Getenv("DB_HOST") == "" {
		if err := godotenv.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка загрузки .env: %v\n", err)
		}
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

	for _, env := range []string{
		"RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASS",
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
	} {
		if os.Getenv(env) == "" {
			fmt.Fprintf(os.Stderr, "Ошибка: Переменная %s не установлена\n", env)
			os.Exit(1)
		}
	}

	fmt.Printf("Assistant подключается к RabbitMQ: %s:%s (user: %s)\n",
		config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser)
	if err := queue.Init(config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser, config.RabbitMQPass); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка инициализации RabbitMQ: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Assistant успешно подключен к RabbitMQ\n")
	defer queue.Close()

	if err := db.Init(config.DBHost, config.DBPort, config.DBUser, config.DBPassword, config.DBName); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка инициализации PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Запускаем постоянный consumer для обработки результатов
	fmt.Printf("🚀 Запуск постоянного consumer'а для обработки результатов\n")
	assistant.startConsumer()

	// НОВАЯ АРХИТЕКТУРА: Один постоянный consumer для всех запросов
	fmt.Printf("🚀 Запуск Assistant с правильной архитектурой\n")

	r := mux.NewRouter()
	r.HandleFunc("/assistant", assistant.processNewCard).Methods("POST")
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"service":   "assistant",
			"timestamp": time.Now().Unix(),
		})
	}).Methods("GET")

	fmt.Fprintf(os.Stderr, "Запуск сервера на :8080\n")
	fmt.Fprintf(os.Stderr, "Assistant запущен с правильной архитектурой\n")
	if err := http.ListenAndServe(":8080", r); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка запуска сервера: %v\n", err)
		os.Exit(1)
	}
}
