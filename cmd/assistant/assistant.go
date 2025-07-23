package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tempizhere/incfactory/internal/api"
	"github.com/tempizhere/incfactory/internal/db"
	"github.com/tempizhere/incfactory/internal/queue"
	"github.com/tempizhere/incfactory/internal/types"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/lib/pq"
)

type PromptConfig struct {
	Prompt           string  `json:"prompt"`
	Temperature      float32 `json:"temperature,omitempty"`
	TopP             float32 `json:"top_p,omitempty"`
	FrequencyPenalty float32 `json:"frequency_penalty,omitempty"`
	PresencePenalty  float32 `json:"presence_penalty,omitempty"`
}

type SearchResult struct {
	CardID     string
	Title      string
	Summary    string
	Solution   string
	Category   string
	Similarity float32
}

type AssistantResult struct {
	SimilarCards        []string `json:"similar_cards"`
	RecommendedSolution string   `json:"recommended_solution"`
}

// cleanText очищает текст от Markdown и нормализует его
func cleanText(text string) string {
	text = strings.ReplaceAll(text, `\u003e`, ">")
	text = strings.ReplaceAll(text, `\"`, "'")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "---", "")
	text = strings.ReplaceAll(text, "##", "")
	text = strings.ReplaceAll(text, "###", "")
	text = strings.ReplaceAll(text, "\n", "; ")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\t", " ")
	reSpaces := regexp.MustCompile(`\s+`)
	text = reSpaces.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// handleError логирует ошибку и отправляет HTTP-ответ
func handleError(w http.ResponseWriter, err error, status int, msg string) {
	fmt.Printf("Ошибка: %v\n", err)
	http.Error(w, msg, status)
}

// float32SliceToString преобразует массив float32 в строку для pgvector
func float32SliceToString(slice []float32) string {
	var parts []string
	for _, v := range slice {
		parts = append(parts, fmt.Sprintf("%f", v))
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ","))
}

// searchSimilarCards выполняет поиск похожих карточек
func searchSimilarCards(description, spaceID string, parentIDs []int, embedding []float32) ([]SearchResult, error) {
	fmt.Printf("Начало поиска похожих карточек, space_id: %s, parentIDs: %v\n", spaceID, parentIDs)
	if len(embedding) != 1024 {
		return nil, fmt.Errorf("некорректная размерность эмбеддинга: %d", len(embedding))
	}

	query := `
		SELECT
			c.card_id,
			c.title,
			s.summary,
			s.solution,
			s.category,
			1 - (v.summary_embedding <=> $1) AS similarity
		FROM incfactory_db.kaiten_cards c
		LEFT JOIN incfactory_db.kaiten_card_summaries s ON c.card_id = s.card_id
		LEFT JOIN incfactory_db.kaiten_summaries_vectors v ON c.card_id = v.card_id
		WHERE c.space_id = $2`
	args := []interface{}{float32SliceToString(embedding), spaceID}

	if len(parentIDs) > 0 {
		query += ` AND c.parents @> $3`
		args = append(args, pq.Array(parentIDs))
	}

	query += `
		ORDER BY similarity DESC
		LIMIT 5`

	fmt.Printf("Выполнение SQL-запроса: %s, args: %v\n", query, args)
	rows, err := db.DB().Query(query, args...)
	if err != nil {
		fmt.Printf("Ошибка SQL-запроса: %v\n", err)
		return nil, fmt.Errorf("ошибка поиска: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.CardID, &r.Title, &r.Summary, &r.Solution, &r.Category, &r.Similarity); err != nil {
			fmt.Printf("Ошибка чтения результата: %v\n", err)
			return nil, fmt.Errorf("ошибка чтения результата: %w", err)
		}
		results = append(results, r)
	}
	fmt.Printf("Найдено %d похожих карточек\n", len(results))
	return results, nil
}

// handleNewCard обрабатывает HTTP-запрос
func handleNewCard(w http.ResponseWriter, r *http.Request) {
	var card api.Card
	if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
		handleError(w, err, http.StatusBadRequest, "Неверный формат данных")
		return
	}

	if card.Description == "" {
		handleError(w, fmt.Errorf("пустое описание"), http.StatusBadRequest, "Описание карточки пустое")
		return
	}

	result, err := processNewCard(card)
	if err != nil {
		handleError(w, err, http.StatusInternalServerError, "Решение не найдено")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode([]AssistantResult{result}); err != nil {
		fmt.Printf("Ошибка кодирования ответа: %v\n", err)
	}
}

// processNewCard обрабатывает карточку
func processNewCard(card api.Card) (AssistantResult, error) {
	var result AssistantResult
	result.SimilarCards = []string{} // Инициализация пустого массива

	// Генерация эмбеддинга
	embTask := types.LLMTask{
		RequestID: fmt.Sprintf("assist-emb-%d", time.Now().UnixNano()),
		Source:    "assistant",
		Type:      "embedding",
		Payload: types.EmbeddingRequest{
			SourceID:   "query",
			SourceType: "query",
			Text:       cleanText(card.Description),
		},
	}
	taskJSON, _ := json.Marshal(embTask)
	fmt.Printf("Отправка embTask: %s\n", string(taskJSON))
	if err := queue.PublishLLMTask(embTask); err != nil {
		fmt.Printf("Ошибка PublishLLMTask: %v\n", err)
		return result, fmt.Errorf("ошибка отправки задачи на эмбеддинг: %w", err)
	}
	fmt.Printf("Задача эмбеддинга отправлена\n")

	// Ожидание эмбеддинга
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	responseChan := make(chan types.LLMResult, 1)
	resultMu.Lock()
	resultChans[embTask.RequestID] = responseChan
	channelTimes[embTask.RequestID] = time.Now()
	resultMu.Unlock()

	// Гарантированная очистка канала эмбеддинга
	defer func() {
		resultMu.Lock()
		delete(resultChans, embTask.RequestID)
		delete(channelTimes, embTask.RequestID)
		resultMu.Unlock()
	}()

	var embedding []float32
	select {
	case llmResult := <-responseChan:
		fmt.Printf("llmResult: request_id=%s, source=%s, type=%s\n", llmResult.RequestID, llmResult.Source, llmResult.Type)
		if llmResult.Type != "embedding" {
			fmt.Printf("Неверный тип результата: %s\n", llmResult.Type)
			return result, fmt.Errorf("неверный тип результата: %s", llmResult.Type)
		}
		var resp types.EmbeddingResponse
		if err := json.Unmarshal(llmResult.Payload, &resp); err != nil {
			fmt.Printf("Ошибка десериализации EmbeddingResponse: %v\n", err)
			return result, fmt.Errorf("ошибка десериализации EmbeddingResponse: %w", err)
		}
		if resp.Error != "" {
			fmt.Printf("Ошибка в EmbeddingResponse: %s\n", resp.Error)
			return result, fmt.Errorf("ошибка эмбеддинга: %s", resp.Error)
		}
		embedding = resp.Embedding
		fmt.Printf("Эмбеддинг получен: %v\n", embedding[:5])
	case <-ctx.Done():
		fmt.Printf("Таймаут ожидания эмбеддинга\n")
		return result, fmt.Errorf("таймаут ожидания эмбеддинга")
	}

	// Подготовка parentIDs
	parentIDs := make([]int, 0, len(card.Parents))
	for _, p := range card.Parents {
		if p.ID > 0 {
			parentIDs = append(parentIDs, p.ID)
		}
	}
	fmt.Printf("Подготовлены parentIDs: %v\n", parentIDs)

	// Поиск похожих карточек
	similarCards, err := searchSimilarCards(card.Description, card.SpaceID, parentIDs, embedding)
	if err != nil {
		fmt.Printf("Ошибка поиска похожих карточек: %v\n", err)
		return result, fmt.Errorf("ошибка поиска похожих карточек: %w", err)
	}

	// Формирование контекста
	var context strings.Builder
	for _, card := range similarCards {
		context.WriteString(fmt.Sprintf("Card ID: %s\nSummary: %s\nSolution: %s\n\n",
			card.CardID, card.Summary, card.Solution))
	}
	fmt.Printf("Контекст сформирован: %s\n", context.String())

	// Загрузка промпта
	promptFile, err := os.ReadFile("config/assistant_prompt.json")
	if err != nil {
		fmt.Printf("Ошибка чтения assistant_prompt.json: %v\n", err)
		return result, fmt.Errorf("ошибка чтения assistant_prompt.json: %v", err)
	}
	var promptConfig PromptConfig
	if err := json.Unmarshal(promptFile, &promptConfig); err != nil {
		fmt.Printf("Ошибка парсинга assistant_prompt.json: %v\n", err)
		return result, fmt.Errorf("ошибка парсинга assistant_prompt.json: %v", err)
	}

	// Формирование LLM-запроса
	prompt := strings.Replace(promptConfig.Prompt, "{query}", cleanText(card.Description), -1)
	prompt = strings.Replace(prompt, "{context}", context.String(), -1)
	messages := []types.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: card.Description},
	}

	recTask := types.LLMTask{
		RequestID: fmt.Sprintf("assist-rec-%d", time.Now().UnixNano()),
		Source:    "assistant",
		Type:      "recommendation",
		Payload: types.LLMRequest{
			CardID:   "assistant_query",
			Messages: messages,
		},
	}
	taskJSON, _ = json.Marshal(recTask)
	fmt.Printf("Отправка recTask: %s\n", string(taskJSON))
	if err := queue.PublishLLMTask(recTask); err != nil {
		fmt.Printf("Ошибка PublishLLMTask для рекомендации: %v\n", err)
		return result, fmt.Errorf("ошибка отправки задачи на рекомендацию: %w", err)
	}
	fmt.Printf("Задача рекомендации отправлена\n")

	// Ожидание рекомендации
	ctx, cancel = context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	responseChan = make(chan types.LLMResult, 1)
	resultMu.Lock()
	resultChans[recTask.RequestID] = responseChan
	channelTimes[recTask.RequestID] = time.Now()
	resultMu.Unlock()

	// Гарантированная очистка канала рекомендации
	defer func() {
		resultMu.Lock()
		delete(resultChans, recTask.RequestID)
		delete(channelTimes, recTask.RequestID)
		resultMu.Unlock()
	}()

	select {
	case llmResult := <-responseChan:
		fmt.Printf("llmResult: request_id=%s, source=%s, type=%s\n", llmResult.RequestID, llmResult.Source, llmResult.Type)
		if llmResult.Type != "recommendation" {
			fmt.Printf("Неверный тип результата рекомендации: %s\n", llmResult.Type)
			return result, fmt.Errorf("неверный тип результата рекомендации: %s", llmResult.Type)
		}
		var resp struct {
			SimilarCards        []struct{ CardID, Similarity string } `json:"similar_cards"`
			RecommendedSolution string                                `json:"recommended_solution"`
		}
		if err := json.Unmarshal(llmResult.Payload, &resp); err != nil {
			fmt.Printf("Ошибка десериализации рекомендации: %v\n", err)
			return result, fmt.Errorf("ошибка десериализации рекомендации: %w", err)
		}
		for _, sc := range resp.SimilarCards {
			result.SimilarCards = append(result.SimilarCards, sc.CardID)
		}
		result.RecommendedSolution = resp.RecommendedSolution
		if len(result.RecommendedSolution) > 500 {
			result.RecommendedSolution = result.RecommendedSolution[:497] + "..."
		}
		if result.RecommendedSolution == "" {
			result.RecommendedSolution = "Решение не найдено"
		}
		fmt.Printf("Рекомендация получена: %+v\n", result)
		return result, nil
	case <-ctx.Done():
		fmt.Printf("Таймаут ожидания рекомендации\n")
		return result, fmt.Errorf("таймаут ожидания рекомендации")
	}
}

var (
	resultChans  = make(map[string]chan types.LLMResult)
	resultMu     sync.Mutex
	channelTimes = make(map[string]time.Time) // Время создания каналов
)

// cleanupOldChannels периодически очищает неиспользуемые каналы
func cleanupOldChannels() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		resultMu.Lock()
		now := time.Now()
		for requestID, createTime := range channelTimes {
			// Удаляем каналы старше 15 минут
			if now.Sub(createTime) > 15*time.Minute {
				fmt.Printf("Очистка устаревшего канала для request_id: %s\n", requestID)
				delete(resultChans, requestID)
				delete(channelTimes, requestID)
			}
		}
		resultMu.Unlock()
	}
}

func consumeLLMResults() {
	msgs, err := queue.ConsumeLLMResults()
	if err != nil {
		fmt.Printf("Ошибка ConsumeLLMResults: %v\n", err)
		os.Exit(1)
	}

	for msg := range msgs {
		fmt.Printf("Получено сообщение из llm_results: %s\n", string(msg.Body))
		var llmResult types.LLMResult
		if err := json.Unmarshal(msg.Body, &llmResult); err != nil {
			fmt.Printf("Ошибка десериализации результата: %v\n", err)
			msg.Nack(false, false)
			continue
		}
		if llmResult.Source != "assistant" {
			fmt.Printf("Неверный источник: %s\n", llmResult.Source)
			msg.Ack(false)
			continue
		}

		resultMu.Lock()
		if ch, ok := resultChans[llmResult.RequestID]; ok {
			select {
			case ch <- llmResult:
				fmt.Printf("Сообщение отправлено в канал для request_id: %s\n", llmResult.RequestID)
				msg.Ack(false)
			default:
				fmt.Printf("Канал для request_id %s переполнен\n", llmResult.RequestID)
				// Канал переполнен, но это может быть нормально (duplicate message)
				// Не отклоняем, чтобы не создавать бесконечный цикл
				msg.Ack(false)
			}
		} else {
			fmt.Printf("Нет канала для request_id %s (возможно, уже обработан)\n", llmResult.RequestID)
			// Канал не найден - это нормально, запрос уже мог быть обработан
			msg.Ack(false)
		}
		resultMu.Unlock()
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка загрузки .env: %v\n", err)
		os.Exit(1)
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

	if err := queue.Init(config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser, config.RabbitMQPass); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка инициализации RabbitMQ: %v\n", err)
		os.Exit(1)
	}
	defer queue.Close()

	if err := db.Init(config.DBHost, config.DBPort, config.DBUser, config.DBPassword, config.DBName); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка инициализации PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	go consumeLLMResults()
	go cleanupOldChannels() // Запуск очистки старых каналов

	r := mux.NewRouter()
	r.HandleFunc("/assistant", handleNewCard).Methods("POST")
	fmt.Fprintf(os.Stderr, "Запуск сервера на :8080\n")
	fmt.Fprintf(os.Stderr, "Запущена очистка неиспользуемых каналов каждые 5 минут\n")
	if err := http.ListenAndServe(":8080", r); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка запуска сервера: %v\n", err)
		os.Exit(1)
	}
}
