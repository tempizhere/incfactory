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

	// НОВАЯ УПРОЩЕННАЯ СХЕМА ОЧЕРЕДЕЙ
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
		// ВОССТАНАВЛИВАЕМ ПРАВИЛЬНУЮ АРХИТЕКТУРУ - РАЗДЕЛЕНИЕ РЕЗУЛЬТАТОВ
		{
			name: "llm_results_assistant",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_results_assistant_dlq",
			},
		},
		{
			name: "llm_results_assistant_dlq",
			args: amqp.Table{
				"x-message-ttl": int32(ttl),
			},
		},
		{
			name: "llm_results_processor",
			args: amqp.Table{
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": "llm_results_processor_dlq",
			},
		},
		{
			name: "llm_results_processor_dlq",
			args: amqp.Table{
				"x-message-ttl": int32(ttl),
			},
		},
		{
			name: "llm_tasks_parking",
			args: amqp.Table{},
		},
	}

	// Объявляем очереди
	for _, q := range queues {
		_, err := pubCh.QueueDeclare(
			q.name,
			true,  // durable
			false, // auto-deleted
			false, // exclusive
			false, // no-wait
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

// Close закрывает соединение с RabbitMQ
func Close() {
	if pubCh != nil {
		pubCh.Close()
	}
	if conn != nil {
		conn.Close()
	}
}

// publishWithConfirm публикует сообщение с подтверждением
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
				fmt.Printf("Сообщение подтверждено RabbitMQ для очереди %s\n", queue)
				return nil
			}
			fmt.Printf("Получен NACK от RabbitMQ для очереди %s (попытка %d/%d)\n", queue, attempt, maxAttempts)
			if attempt == maxAttempts {
				return fmt.Errorf("publisher nack")
			}
		case <-time.After(5 * time.Second):
			fmt.Printf("Timeout подтверждения от RabbitMQ для очереди %s (попытка %d/%d)\n", queue, attempt, maxAttempts)
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

// PublishCardWithComments публикует карточку с комментариями в очередь kaiten_transactions
func PublishCardWithComments(msg CardWithComments) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}
	return publishWithConfirm("kaiten_transactions", body)
}

// PublishLLMResult публикует результат обработки LLM
func PublishLLMResult(msg types.LLMResult) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ошибка сериализации: %w", err)
	}

	// ВОССТАНАВЛИВАЕМ ПРАВИЛЬНУЮ ЛОГИКУ: Отправляем в соответствующую очередь по source
	var queueName string
	switch msg.Source {
	case "assistant":
		queueName = "llm_results_assistant"
	case "processor":
		queueName = "llm_results_processor"
	default:
		queueName = "llm_results_assistant" // fallback
	}

	fmt.Printf("Публикуем в очередь %s: request_id=%s, correlation_id=%s, type=%s, source=%s\n",
		queueName, msg.RequestID, msg.CorrelationID, msg.Type, msg.Source)

	return publishWithConfirm(queueName, body)
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

// ConsumeLLMResults потребляет результаты обработки LLM для assistant
func ConsumeLLMResults() (<-chan amqp.Delivery, error) {
	fmt.Printf("Создание потребителя для очереди llm_results_assistant\n")
	consCh, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия канала: %w", err)
	}
	if err := consCh.Qos(getPrefetch(), 0, false); err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка установки QoS: %w", err)
	}

	// Генерируем уникальный consumer tag
	consumerTag := fmt.Sprintf("assistant-consumer-%d", time.Now().UnixNano())
	fmt.Printf("Используем consumer tag: %s\n", consumerTag)

	msgs, err := consCh.Consume(
		"llm_results_assistant",
		consumerTag,
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_results_assistant: %w", err)
	}
	fmt.Printf("Потребитель для очереди llm_results_assistant успешно создан с tag: %s\n", consumerTag)
	return msgs, nil
}

// ConsumeLLMResultsProcessor потребляет результаты обработки LLM для processor
func ConsumeLLMResultsProcessor() (<-chan amqp.Delivery, error) {
	fmt.Printf("Создание потребителя для очереди llm_results_processor\n")
	consCh, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия канала: %w", err)
	}
	if err := consCh.Qos(getPrefetch(), 0, false); err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка установки QoS: %w", err)
	}

	// Генерируем уникальный consumer tag
	consumerTag := fmt.Sprintf("processor-consumer-%d", time.Now().UnixNano())
	fmt.Printf("Используем consumer tag: %s\n", consumerTag)

	msgs, err := consCh.Consume(
		"llm_results_processor",
		consumerTag,
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		consCh.Close()
		return nil, fmt.Errorf("ошибка регистрации потребителя llm_results_processor: %w", err)
	}
	fmt.Printf("Потребитель для очереди llm_results_processor успешно создан с tag: %s\n", consumerTag)
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
