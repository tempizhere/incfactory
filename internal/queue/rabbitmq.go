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
	conn  *amqp.Connection
	pubCh *amqp.Channel
	confs chan amqp.Confirmation
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

	pubCh, err = conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("ошибка открытия канала: %w", err)
	}

	// Publisher confirms
	if err := pubCh.Confirm(false); err != nil {
		pubCh.Close()
		conn.Close()
		return fmt.Errorf("не удалось включить publisher confirms: %w", err)
	}
	confs = pubCh.NotifyPublish(make(chan amqp.Confirmation, 1))

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
		{
			name: "llm_tasks_parking",
			args: amqp.Table{},
		},
	}

	for _, q := range queues {
		_, err = pubCh.QueueDeclare(
			q.name,
			true,
			false,
			false,
			false,
			q.args,
		)
		if err != nil {
			pubCh.Close()
			conn.Close()
			return fmt.Errorf("ошибка объявления очереди %s: %w", q.name, err)
		}
		fmt.Printf("Очередь %s успешно объявлена\n", q.name)
	}

	return nil
}

// Close закрывает соединение и канал
func Close() {
	if pubCh != nil {
		if err := pubCh.Close(); err != nil {
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
	return publishWithConfirm("kaiten_transactions", body)
}

// publishWithConfirm публикует сообщение с персистентностью и подтверждениями
func publishWithConfirm(queue string, body []byte) error {
	if pubCh == nil {
		return fmt.Errorf("канал публикации не инициализирован")
	}
	publish := func() error {
		return pubCh.Publish(
			"",
			queue,
			false,
			false,
			amqp.Publishing{
				ContentType:  "application/json",
				DeliveryMode: amqp.Persistent,
				Body:         body,
			},
		)
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := publish(); err != nil {
			if attempt == maxAttempts {
				return err
			}
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		select {
		case conf := <-confs:
			if conf.Ack {
				return nil
			}
			if attempt == maxAttempts {
				return fmt.Errorf("publisher nack")
			}
		case <-time.After(5 * time.Second):
			if attempt == maxAttempts {
				return fmt.Errorf("publisher confirm timeout")
			}
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return fmt.Errorf("не удалось опубликовать сообщение")
}

// PublishLLMTask публикует запрос на обработку LLM
func PublishLLMTask(msg types.LLMTask) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}
	return publishWithConfirm("llm_tasks", body)
}

// PublishLLMResult публикует результат обработки LLM
func PublishLLMResult(msg types.LLMResult) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}
	return publishWithConfirm("llm_results", body)
}

// PublishToQueue публикует произвольное сообщение в указанную очередь
func PublishToQueue(queueName string, body []byte) error {
	return publishWithConfirm(queueName, body)
}

// ConsumeCardWithComments потребляет сообщения с карточками и комментариями
func ConsumeCardWithComments() (<-chan amqp.Delivery, error) {
	consCh, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия канала: %w", err)
	}
	if err := consCh.Qos(getPrefetch(), 0, false); err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка установки QoS: %w", err)
	}
	msgs, err := consCh.Consume(
		"kaiten_transactions",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка регистрации потребителя kaiten_transactions: %w", err)
	}
	return msgs, nil
}

// ConsumeLLMTasks потребляет запросы на обработку LLM
func ConsumeLLMTasks() (<-chan amqp.Delivery, error) {
	consCh, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия канала: %w", err)
	}
	if err := consCh.Qos(getPrefetch(), 0, false); err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка установки QoS: %w", err)
	}
	msgs, err := consCh.Consume(
		"llm_tasks",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_tasks: %w", err)
	}
	return msgs, nil
}

// ConsumeLLMResults потребляет результаты обработки LLM
func ConsumeLLMResults() (<-chan amqp.Delivery, error) {
	consCh, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия канала: %w", err)
	}
	if err := consCh.Qos(getPrefetch(), 0, false); err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка установки QoS: %w", err)
	}
	msgs, err := consCh.Consume(
		"llm_results",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_results: %w", err)
	}
	return msgs, nil
}

func getPrefetch() int {
	val := os.Getenv("RABBITMQ_PREFETCH")
	if val == "" {
		return 10
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return 10
	}
	return n
}
