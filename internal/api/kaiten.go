package api

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Card struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	BoardID     int    `json:"board_id"`
	ColumnID    int    `json:"column_id"`
	SpaceID     string `json:"space_id,omitempty"`
	Parents     []struct {
		ID int `json:"id"`
	} `json:"parents"`
	Children []struct {
		ID int `json:"id"`
	} `json:"children"`
	Created string `json:"created"`
	Updated string `json:"updated"` // Добавлено поле updated
}

type Comment struct {
	ID      int    `json:"id"`
	Text    string `json:"text"`
	Author  Author `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"` // Добавлено поле updated
}

type Author struct {
	Username string `json:"username"`
}

var (
	httpClient           *http.Client
	apiToken             string
	baseURL              string
	kaitenMaxRetries     int
	kaitenRetryBaseDelay float64
)

func Init(kaitenHost, kaitenAPIKey string) {
	baseURL = kaitenHost
	if !strings.HasPrefix(kaitenAPIKey, "Bearer ") {
		apiToken = "Bearer " + kaitenAPIKey
	} else {
		apiToken = kaitenAPIKey
	}

	// HTTP client with connection pooling
	httpClient = &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Retry config from ENV
	kaitenMaxRetries = 5
	if v := strings.TrimSpace(getEnv("KAITEN_MAX_RETRIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			kaitenMaxRetries = n
		}
	}
	kaitenRetryBaseDelay = 1.0
	if v := strings.TrimSpace(getEnv("KAITEN_RETRY_BASE_DELAY")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			kaitenRetryBaseDelay = f
		}
	}
}

func getEnv(k string) string { return os.Getenv(k) }

func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second, true
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d, true
		}
	}
	return 0, false
}

func backoffDelay(base float64, attempt int) time.Duration {
	// экспоненциальный рост: base * 2^(attempt-1)
	d := base * math.Pow(2, float64(attempt-1))
	if d < 0.1 {
		d = 0.1
	}
	if d > 30 {
		d = 30
	}
	// jitter +-25%
	jitter := d * 0.25
	return time.Duration((d - jitter + (2*jitter)*randFloat()) * float64(time.Second))
}

func randFloat() float64 { return float64(time.Now().UnixNano()%9973) / 9973.0 }

func doRequest(method, urlStr string) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= kaitenMaxRetries; attempt++ {
		req, err := http.NewRequest(method, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("ошибка создания запроса: %w", err)
		}
		req.Header.Set("Authorization", apiToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("ошибка выполнения запроса: %w", err)
		} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		} else {
			// Read body for logging and close
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("ошибка API (%d): %s", resp.StatusCode, string(body))
				// Retry with Retry-After if present
				if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
					time.Sleep(ra)
				} else {
					time.Sleep(backoffDelay(kaitenRetryBaseDelay, attempt))
				}
				continue
			}
			// Non-retryable 4xx
			return nil, fmt.Errorf("ошибка API (%d): %s", resp.StatusCode, string(body))
		}
		// network error branch: backoff
		if attempt < kaitenMaxRetries {
			time.Sleep(backoffDelay(kaitenRetryBaseDelay, attempt))
			continue
		}
	}
	return nil, lastErr
}

func FetchCardBatch(spaceID, columnID string, limit, offset int, startDate time.Time) ([]Card, error) {
	u, err := url.Parse(baseURL + "/api/latest/cards/")
	if err != nil {
		return nil, fmt.Errorf("ошибка разбора URL: %w", err)
	}
	q := u.Query()
	q.Set("space_id", spaceID)
	q.Set("column_id", columnID)
	q.Set("additional_card_fields", "description,board_id,column_id,parents,children,created,updated")
	q.Set("created_after", startDate.Format(time.RFC3339))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	resp, err := doRequest(http.MethodGet, u.String())
	if err != nil {
		return nil, fmt.Errorf("ошибка получения карточек: %w", err)
	}
	defer resp.Body.Close()

	var cards []Card
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		return nil, fmt.Errorf("ошибка декодирования карточек: %w", err)
	}

	// Устанавливаем space_id для каждой карточки
	for i := range cards {
		cards[i].SpaceID = spaceID
	}

	return cards, nil
}

func FetchComments(cardID int) ([]Comment, error) {
	urlStr := fmt.Sprintf("%s/api/latest/cards/%d/comments", baseURL, cardID)
	resp, err := doRequest(http.MethodGet, urlStr)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return []Comment{}, nil
		}
		return nil, fmt.Errorf("ошибка получения комментариев: %w", err)
	}
	defer resp.Body.Close()

	var comments []Comment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return nil, fmt.Errorf("ошибка декодирования комментариев: %w", err)
	}

	return comments, nil
}
