package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/tempizhere/incfactory/internal/api"
	"github.com/tempizhere/incfactory/internal/queue"
	"golang.org/x/time/rate"
)

func main() {
	// Загружаем .env только если основные переменные не установлены
	if os.Getenv("KAITEN_HOST") == "" || os.Getenv("RABBITMQ_HOST") == "" {
		if err := godotenv.Load(); err != nil {
			fmt.Println("Ошибка загрузки .env:", err)
		}
	}

	config := struct {
		KaitenHost      string
		KaitenAPIKey    string
		RabbitMQHost    string
		RabbitMQPort    string
		RabbitMQUser    string
		RabbitMQPass    string
		SpaceID         string
		ColumnID        string
		Limit           string
		ExcludedAuthors []string
	}{
		KaitenHost:      os.Getenv("KAITEN_HOST"),
		KaitenAPIKey:    os.Getenv("KAITEN_API_KEY"),
		RabbitMQHost:    os.Getenv("RABBITMQ_HOST"),
		RabbitMQPort:    os.Getenv("RABBITMQ_PORT"),
		RabbitMQUser:    os.Getenv("RABBITMQ_USER"),
		RabbitMQPass:    os.Getenv("RABBITMQ_PASS"),
		SpaceID:         os.Getenv("SPACE_ID"),
		ColumnID:        os.Getenv("COLUMN_ID"),
		Limit:           os.Getenv("LIMIT"),
		ExcludedAuthors: strings.Split(os.Getenv("EXCLUDED_COMMENT_AUTHORS"), ","),
	}

	required := []string{"KAITEN_HOST", "KAITEN_API_KEY", "RABBITMQ_HOST", "RABBITMQ_PORT", "RABBITMQ_USER", "RABBITMQ_PASS", "SPACE_ID", "COLUMN_ID", "LIMIT"}
	for _, env := range required {
		if os.Getenv(env) == "" {
			fmt.Printf("Ошибка: Переменная %s не установлена\n", env)
			return
		}
	}

	limit, err := strconv.Atoi(config.Limit)
	if err != nil || limit <= 0 {
		fmt.Printf("Ошибка: LIMIT должен быть положительным числом, получено: %s\n", config.Limit)
		return
	}

	startDateStr := flag.String("start-date", "", "Дата начала выгрузки (YYYY-MM-DD)")
	flag.Parse()

	var startDate time.Time
	if *startDateStr != "" {
		startDate, err = time.Parse("2006-01-02", *startDateStr)
		if err != nil {
			fmt.Printf("Ошибка парсинга --start-date: %v\n", err)
			return
		}
	} else {
		startDate = time.Now().UTC().AddDate(0, 0, -3)
	}

	api.Init(config.KaitenHost, config.KaitenAPIKey)

	if err := queue.Init(config.RabbitMQHost, config.RabbitMQPort, config.RabbitMQUser, config.RabbitMQPass); err != nil {
		fmt.Printf("Ошибка инициализации RabbitMQ: %v\n", err)
		return
	}
	defer queue.Close()

	fmt.Printf("Получение карточек с %s...\n", startDate.Format("2006-01-02"))

	// Kaiten rate limiter
	rps := 1.0
	if v := os.Getenv("KAITEN_RATE_LIMIT_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			rps = f
		}
	}
	limiter := rate.NewLimiter(rate.Limit(rps), 1)

	offset := 0
	for {
		// лимитируем запрос к карточкам
		_ = limiter.Wait(context.Background())
		cards, err := api.FetchCardBatch(config.SpaceID, config.ColumnID, limit, offset, startDate)
		if err != nil {
			fmt.Printf("Ошибка получения карточек (offset=%d): %v\n", offset, err)
			// мягкий бэкофф и продолжение
			time.Sleep(2 * time.Second)
			continue
		}

		if len(cards) == 0 {
			fmt.Println("Больше карточек не найдено, остановка пагинации.")
			break
		}

		fmt.Printf("Получено %d карточек в партии (offset=%d).\n", len(cards), offset)
		for _, card := range cards {
			// лимитируем запрос к комментариям
			_ = limiter.Wait(context.Background())
			comments, err := api.FetchComments(card.ID)
			if err != nil {
				fmt.Printf("Ошибка получения комментариев для карточки %d: %v\n", card.ID, err)
				continue
			}

			var filteredComments []api.Comment
			for _, comment := range comments {
				// Фильтрация по авторам
				isExcluded := false
				for _, excluded := range config.ExcludedAuthors {
					if strings.EqualFold(comment.Author.Username, excluded) {
						isExcluded = true
						fmt.Printf("Пропущен комментарий %d от %s для карточки %d\n", comment.ID, comment.Author.Username, card.ID)
						break
					}
				}
				if isExcluded {
					continue
				}

				// Фильтрация по пустому тексту
				trimmedText := strings.TrimSpace(comment.Text)
				if trimmedText == "" {
					fmt.Printf("Пропущен комментарий %d для карточки %d: текст пустой\n", comment.ID, card.ID)
					continue
				}

				// Фильтрация по длине текста (< 13 байт)
				if len(trimmedText) < 13 {
					fmt.Printf("Пропущен комментарий %d для карточки %d: текст короче 6 символов (%s)\n", comment.ID, card.ID, trimmedText)
					continue
				}

				filteredComments = append(filteredComments, comment)
			}

			cardMsg := queue.CardWithComments{
				Card:     card,
				Comments: filteredComments,
			}

			// Отправляем карточку с комментариями в RabbitMQ сразу
			if err := queue.PublishCardWithComments(cardMsg); err != nil {
				fmt.Printf("Ошибка отправки сообщения для карточки %d в RabbitMQ: %v\n", cardMsg.Card.ID, err)
			} else {
				fmt.Printf("Отправлено сообщение для карточки %d в RabbitMQ\n", cardMsg.Card.ID)
			}
		}
		offset += limit
		// короткая пауза между страницами
		time.Sleep(200 * time.Millisecond)
		if len(cards) < limit {
			fmt.Println("Получено меньше карточек, чем лимит, конец результатов.")
			break
		}
	}

	fmt.Println("Завершено")
}
