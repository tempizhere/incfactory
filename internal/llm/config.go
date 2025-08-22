package llm

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var (
	embeddingClient        *http.Client
	embeddingHost          string
	embeddingAPIKey        string
	embeddingProvider      string
	embeddingModel         string
	embeddingMaxRetries    int
	embeddingRetryDelay    float64
	embeddingMaxTextLength int

	completionClient   *http.Client
	completionHost     string
	completionAPIKey   string
	completionProvider string
	completionModel    string
)

// Init инициализирует конфигурацию для эндпоинтов эмбеддингов и завершения текста
func Init(embHost, embAPIKey, embProvider, embModel, compHost, compAPIKey, compProvider, compModel string, embMaxRetries int, embRetryDelay float64, embMaxTextLength int) {
	// Инициализация эмбеддингов
	u, err := url.Parse(embHost)
	if err != nil {
		panic(fmt.Errorf("ошибка парсинга EMBEDDING_HOST: %w", err))
	}
	if u.Scheme == "" || u.Host == "" {
		panic(fmt.Errorf("некорректный EMBEDDING_HOST: %s, ожидается https://host или http://host", embHost))
	}
	embeddingHost = embHost
	embeddingAPIKey = embAPIKey
	embeddingProvider = embProvider
	embeddingModel = embModel
	embeddingMaxRetries = embMaxRetries
	embeddingRetryDelay = embRetryDelay
	embeddingMaxTextLength = embMaxTextLength

	// Создаем HTTP клиент с пулом соединений для эмбеддингов
	embeddingClient = &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  false,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Инициализация завершения текста
	u, err = url.Parse(compHost)
	if err != nil {
		panic(fmt.Errorf("ошибка парсинга LLM_HOST: %w", err))
	}
	if u.Scheme == "" || u.Host == "" {
		panic(fmt.Errorf("некорректный LLM_HOST: %s, ожидается https://host или http://host", compHost))
	}
	completionHost = compHost
	completionAPIKey = compAPIKey
	completionProvider = compProvider
	completionModel = compModel

	// Создаем HTTP клиент для завершения текста
	completionClient = &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  false,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
}
