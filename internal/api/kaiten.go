package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	httpClient = &http.Client{Timeout: 30 * time.Second}
	apiToken   string
	baseURL    string
)

func Init(kaitenHost, kaitenAPIKey string) {
	baseURL = kaitenHost
	if !strings.HasPrefix(kaitenAPIKey, "Bearer ") {
		apiToken = "Bearer " + kaitenAPIKey
	} else {
		apiToken = kaitenAPIKey
	}
}

func doRequest(method, urlStr string) (*http.Response, error) {
	req, err := http.NewRequest(method, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса: %w", err)
	}
	req.Header.Set("Authorization", apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения запроса: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("ошибка API (%d): %s", resp.StatusCode, string(body))
	}
	return resp, nil
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
