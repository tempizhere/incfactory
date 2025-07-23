package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/tempizhere/incfactory/internal/api"
	"github.com/tempizhere/incfactory/internal/types"

	amqp "github.com/rabbitmq/amqp091-go"
)

type CardWithComments struct {
	Card     api.Card      `json:"card"`
	Comments []api.Comment `json:"comments"`
}

var (
	conn *amqp.Connection
	ch   *amqp.Channel
)

// Init инициализирует подключение к RabbitMQ и объявляет очереди
func Init(host, port, user, pass string) error {
	ttlStr := os.Getenv("RABBITMQ_DEAD_LETTER_TTL")
	ttl, err := strconv.ParseInt(ttlStr, 10, 64)
	if err != nil || ttl <= 0 {
		ttl = 3600 * 1000 // Default: 1 hour in milliseconds
	}

	var connErr error
	for i := 0; i < 3; i++ {
		conn, connErr = amqp.Dial(fmt.Sprintf("amqp://%s:%s@%s:%s/", user, pass, host, port))
		if connErr == nil {
			break
		}
		fmt.Printf("Ошибка подключения к RabbitMQ (попытка %d): %v\n", i+1, connErr)
		time.Sleep(2 * time.Second)
	}
	if connErr != nil {
		return fmt.Errorf("ошибка подключения к RabbitMQ после 3 попыток: %w", connErr)
	}

	ch, err = conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("ошибка открытия канала: %w", err)
	}

	queues := []struct {
		name string
		args amqp.Table
	}{
		{
			name: "kaiten_transactions",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "kaiten_transactions_dlq",
			},
		},
		{
			name: "kaiten_transactions_dlq",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "kaiten_transactions_retry",
				"x-message-ttl":             int32(ttl),
			},
		},
		{
			name: "kaiten_transactions_retry",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "kaiten_transactions",
				"x-message-ttl":             int32(ttl),
			},
		},
		{
			name: "llm_tasks",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_tasks_dlq",
			},
		},
		{
			name: "llm_tasks_dlq",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_tasks_retry",
				"x-message-ttl":             int32(ttl),
			},
		},
		{
			name: "llm_tasks_retry",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_tasks",
				"x-message-ttl":             int32(ttl),
			},
		},
		{
			name: "llm_results",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_results_dlq",
			},
		},
		{
			name: "llm_results_dlq",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_results_retry",
				"x-message-ttl":             int32(ttl),
			},
		},
		{
			name: "llm_results_retry",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_results",
				"x-message-ttl":             int32(ttl),
			},
		},
	}

	for _, q := range queues {
		_, err = ch.QueueDeclare(
			q.name,
			true,
			false,
			false,
			false,
			q.args,
		)
		if err != nil {
			ch.Close()
			conn.Close()
			return fmt.Errorf("ошибка объявления очереди %s: %w", q.name, err)
		}
		fmt.Printf("Очередь %s успешно объявлена\n", q.name)
	}

	return nil
}

// Close закрывает соединение и канал
func Close() {
	if ch != nil {
		if err := ch.Close(); err != nil {
			fmt.Printf("Ошибка закрытия канала: %v\n", err)
		}
	}
	if conn != nil {
		if err := conn.Close(); err != nil {
			fmt.Printf("Ошибка закрытия соединения: %v\n", err)
		}
	}
}

// PublishCardWithComments публикует сообщение с карточкой и комментариями
func PublishCardWithComments(msg CardWithComments) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	err = ch.Publish(
		"",
		"kaiten_transactions",
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		},
	)
	if err != nil {
		return fmt.Errorf("ошибка публикации в kaiten_transactions: %w", err)
	}
	return nil
}

// PublishLLMTask публикует запрос на обработку LLM
func PublishLLMTask(msg types.LLMTask) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	err = ch.Publish(
		"",
		"llm_tasks",
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		},
	)
	if err != nil {
		return fmt.Errorf("ошибка публикации в llm_tasks: %w", err)
	}
	return nil
}

// PublishLLMResult публикует результат обработки LLM
func PublishLLMResult(msg types.LLMResult) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	err = ch.Publish(
		"",
		"llm_results",
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		},
	)
	if err != nil {
		return fmt.Errorf("ошибка публикации в llm_results: %w", err)
	}
	return nil
}

// ConsumeCardWithComments потребляет сообщения с карточками и комментариями
func ConsumeCardWithComments() (<-chan amqp.Delivery, error) {
	msgs, err := ch.Consume(
		"kaiten_transactions",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ошибка регистрации потребителя kaiten_transactions: %w", err)
	}
	return msgs, nil
}

// ConsumeLLMTasks потребляет запросы на обработку LLM
func ConsumeLLMTasks() (<-chan amqp.Delivery, error) {
	msgs, err := ch.Consume(
		"llm_tasks",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_tasks: %w", err)
	}
	return msgs, nil
}

// ConsumeLLMResults потребляет результаты обработки LLM
func ConsumeLLMResults() (<-chan amqp.Delivery, error) {
	msgs, err := ch.Consume(
		"llm_results",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_results: %w", err)
	}
	return msgs, nil
}
