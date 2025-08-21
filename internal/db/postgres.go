package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/tempizhere/incfactory/internal/api"
)

var db *sql.DB

// Init инициализирует подключение к PostgreSQL
func Init(host, port, user, password, name string) error {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, name)
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("ошибка подключения к PostgreSQL: %v", err)
	}
	if err = db.Ping(); err != nil {
		return fmt.Errorf("ошибка проверки подключения: %v", err)
	}
	return nil
}

// Close закрывает соединение
func Close() {
	if db != nil {
		db.Close()
	}
}

// DB возвращает указатель на базу
func DB() *sql.DB {
	return db
}

// SaveCard сохраняет карточку
func SaveCard(card api.Card) error {
	created, err := time.Parse(time.RFC3339, card.Created)
	if err != nil {
		return fmt.Errorf("ошибка парсинга created: %v", err)
	}
	updated, err := time.Parse(time.RFC3339, card.Updated)
	if err != nil {
		return fmt.Errorf("ошибка парсинга updated: %v", err)
	}

	parents := make([]int, len(card.Parents))
	for i, p := range card.Parents {
		parents[i] = p.ID
	}
	children := make([]int, len(card.Children))
	for i, c := range card.Children {
		children[i] = c.ID
	}

	_, err = db.Exec(`
		INSERT INTO incfactory_db.kaiten_cards (card_id, title, description, board_id, column_id, space_id, parents, children, created, updated)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (card_id) DO UPDATE
		SET title = EXCLUDED.title, description = EXCLUDED.description, board_id = EXCLUDED.board_id,
			column_id = EXCLUDED.column_id, space_id = EXCLUDED.space_id, parents = EXCLUDED.parents,
			children = EXCLUDED.children, created = EXCLUDED.created, updated = EXCLUDED.updated`,
		fmt.Sprintf("%d", card.ID), card.Title, card.Description, fmt.Sprintf("%d", card.BoardID),
		fmt.Sprintf("%d", card.ColumnID), card.SpaceID, pq.Array(parents), pq.Array(children), created, updated)
	return err
}

// SaveComment сохраняет комментарий
func SaveComment(comment api.Comment, cardID int) error {
	var exists bool
	err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM incfactory_db.kaiten_cards WHERE card_id = $1)", fmt.Sprintf("%d", cardID)).Scan(&exists)
	if err != nil {
		return fmt.Errorf("ошибка проверки карточки %d: %v", cardID, err)
	}
	if !exists {
		return fmt.Errorf("карточка %d не найдена", cardID)
	}

	created, err := time.Parse(time.RFC3339, comment.Created)
	if err != nil {
		return fmt.Errorf("ошибка парсинга created: %v", err)
	}
	updated, err := time.Parse(time.RFC3339, comment.Updated)
	if err != nil {
		return fmt.Errorf("ошибка парсинга updated: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO incfactory_db.kaiten_comments (comment_id, card_id, text, author, created, updated)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (comment_id) DO UPDATE
		SET text = EXCLUDED.text, author = EXCLUDED.author, created = EXCLUDED.created, updated = EXCLUDED.updated`,
		fmt.Sprintf("%d", comment.ID), fmt.Sprintf("%d", cardID), comment.Text, comment.Author.Username, created, updated)
	return err
}

// GetCommentsByCardID возвращает комментарии по card_id
func GetCommentsByCardID(cardID string) ([]api.Comment, error) {
	rows, err := db.Query(`
		SELECT comment_id, card_id, text, author, created, updated
		FROM incfactory_db.kaiten_comments
		WHERE card_id = $1`, cardID)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения комментариев для карточки %s: %v", cardID, err)
	}
	defer rows.Close()

	var comments []api.Comment
	for rows.Next() {
		var comment api.Comment
		var created, updated time.Time
		var cardID string
		if err := rows.Scan(&comment.ID, &cardID, &comment.Text, &comment.Author.Username, &created, &updated); err != nil {
			return nil, fmt.Errorf("ошибка сканирования комментария: %v", err)
		}
		comment.Created = created.Format(time.RFC3339)
		comment.Updated = updated.Format(time.RFC3339)
		comments = append(comments, comment)
	}
	return comments, nil
}

// FindCardsWithSharedParents находит карточки с общими родителями
func FindCardsWithSharedParents(currentCardID string, parentIDs []int) ([]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT card_id
		FROM incfactory_db.kaiten_cards
		WHERE card_id != $1
		AND parents && $2
	`
	rows, err := db.Query(query, currentCardID, pq.Array(parentIDs))
	if err != nil {
		return nil, fmt.Errorf("ошибка поиска карточек с общими родителями: %v", err)
	}
	defer rows.Close()

	var cardIDs []string
	for rows.Next() {
		var cardID string
		if err := rows.Scan(&cardID); err != nil {
			return nil, fmt.Errorf("ошибка сканирования card_id: %v", err)
		}
		cardIDs = append(cardIDs, cardID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ошибка итерации результата: %v", err)
	}

	return cardIDs, nil
}

// CheckSummaryExists проверяет существование суммарии
func CheckSummaryExists(cardID string) (bool, error) {
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM incfactory_db.kaiten_card_summaries WHERE card_id = $1)`, cardID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("ошибка проверки суммарии %s: %v", cardID, err)
	}
	return exists, nil
}

// SaveCardSummary сохраняет суммарию
func SaveCardSummary(cardID, summary, solution, category string) error {
	_, err := db.Exec(`
		INSERT INTO incfactory_db.kaiten_card_summaries (card_id, summary, solution, category, created, updated)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (card_id) DO UPDATE
		SET summary = EXCLUDED.summary, solution = EXCLUDED.solution, category = EXCLUDED.category,
			updated = EXCLUDED.updated`,
		cardID, summary, solution, category, time.Now())
	if err != nil {
		return fmt.Errorf("ошибка сохранения суммарии %s: %v", cardID, err)
	}
	return nil
}

// SaveSummaryEmbedding сохраняет эмбеддинг суммарии
func SaveSummaryEmbedding(sourceID, sourceType string, embedding []float32, text string) error {
	if err := UpdateVectorSchemaIfNeeded(len(embedding)); err != nil {
		return fmt.Errorf("ошибка обновления базы: %v", err)
	}

	cardID := strings.TrimSuffix(sourceID, "_"+sourceType)
	embeddingStr := fmt.Sprintf("[%s]", float32SliceToString(embedding))

	if sourceType == "summary" {
		_, err := db.Exec(`
			INSERT INTO incfactory_db.kaiten_summaries_vectors (card_id, summary_embedding, created_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (card_id) DO UPDATE
			SET summary_embedding = EXCLUDED.summary_embedding, created_at = EXCLUDED.created_at`,
			cardID, embeddingStr, time.Now())
		if err != nil {
			return fmt.Errorf("ошибка сохранения эмбеддинга summary %s: %v", cardID, err)
		}
	} else {
		_, err := db.Exec(`
			INSERT INTO incfactory_db.kaiten_summaries_vectors (card_id, solution_embedding, created_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (card_id) DO UPDATE
			SET solution_embedding = EXCLUDED.solution_embedding, created_at = EXCLUDED.created_at`,
			cardID, embeddingStr, time.Now())
		if err != nil {
			return fmt.Errorf("ошибка сохранения эмбеддинга solution %s: %v", cardID, err)
		}
	}
	return nil
}

// CheckCardExists проверяет существование карточки и изменения по updated
func CheckCardExists(card api.Card) (exists bool, updatedChanged bool, err error) {
	var dbUpdated time.Time
	err = db.QueryRow(`
		SELECT updated
		FROM incfactory_db.kaiten_cards
		WHERE card_id = $1`, fmt.Sprintf("%d", card.ID)).Scan(&dbUpdated)
	if err == sql.ErrNoRows {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("ошибка проверки карточки %d: %v", card.ID, err)
	}

	updated, err := time.Parse(time.RFC3339, card.Updated)
	if err != nil {
		return true, true, fmt.Errorf("ошибка парсинга updated для карточки %d: %v", card.ID, err)
	}

	return true, !updated.Equal(dbUpdated), nil
}

// CheckCommentExists проверяет существование комментария и изменения по updated
func CheckCommentExists(comment api.Comment, cardID int) (exists bool, updatedChanged bool, err error) {
	var dbUpdated time.Time
	err = db.QueryRow(`
		SELECT updated
		FROM incfactory_db.kaiten_comments
		WHERE comment_id = $1`, fmt.Sprintf("%d", comment.ID)).Scan(&dbUpdated)
	if err == sql.ErrNoRows {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("ошибка проверки комментария %d: %v", comment.ID, err)
	}

	updated, err := time.Parse(time.RFC3339, comment.Updated)
	if err != nil {
		return true, true, fmt.Errorf("ошибка парсинга updated для комментария %d: %v", comment.ID, err)
	}

	return true, !updated.Equal(dbUpdated), nil
}

// CheckCardExistsByID проверяет существование карточки по ID
func CheckCardExistsByID(cardID string) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM incfactory_db.kaiten_cards WHERE card_id = $1)", cardID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("ошибка проверки карточки %s: %v", cardID, err)
	}
	return exists, nil
}

// UpdateVectorSchemaIfNeeded обновляет схему векторных таблиц
func UpdateVectorSchemaIfNeeded(dim int) error {
	var currentDim int
	err := db.QueryRow(`
		SELECT atttypmod
		FROM pg_attribute
		WHERE attrelid = 'incfactory_db.kaiten_summaries_vectors'::regclass AND attname = 'summary_embedding'
	`).Scan(&currentDim)
	if err != nil {
		return fmt.Errorf("ошибка получения размерности вектора: %v", err)
	}

	if currentDim == dim {
		return nil
	}

	log.Printf("Обновление размерности вектора с %d на %d", currentDim, dim)

	_, err = db.Exec(`
		DROP TABLE IF EXISTS incfactory_db.kaiten_summaries_vectors;
	`)
	if err != nil {
		return fmt.Errorf("ошибка удаления таблиц: %v", err)
	}

	_, err = db.Exec(fmt.Sprintf(`
		CREATE TABLE incfactory_db.kaiten_summaries_vectors (
			id SERIAL PRIMARY KEY,
			card_id VARCHAR(50) NOT NULL UNIQUE,
			summary_embedding VECTOR(%d),
			solution_embedding VECTOR(%d),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
		);
	`, dim, dim))
	if err != nil {
		return fmt.Errorf("ошибка создания таблиц: %v", err)
	}

	log.Printf("Схема таблиц обновлена")
	return nil
}

// float32SliceToString преобразует массив float32 в строку для pgvector
func float32SliceToString(slice []float32) string {
	var parts []string
	for _, v := range slice {
		parts = append(parts, fmt.Sprintf("%f", v))
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ","))
}

// SearchResult представляет результат поиска похожих карточек
type SearchResult struct {
	ID         string
	Title      string
	Summary    string
	Solution   string
	Category   string
	Similarity float32
}

// FindSimilarCards выполняет поиск похожих карточек по эмбеддингу
func FindSimilarCards(embedding []float32, limit int) ([]SearchResult, error) {
	if len(embedding) != 1024 {
		return nil, fmt.Errorf("некорректная размерность эмбеддинга: %d", len(embedding))
	}

	// Параметры поиска
	wSummary := 0.7
	wSolution := 0.3
	minSim := 0.25

	// Формируем основной запрос
	query := `
		SELECT
			c.card_id,
			c.title,
			s.summary,
			s.solution,
			s.category,
			(
				COALESCE(1 - (v.summary_embedding  <=> $1), 0) * $2::FLOAT +
				COALESCE(1 - (v.solution_embedding <=> $1), 0) * $3::FLOAT
			) AS similarity
		FROM incfactory_db.kaiten_cards c
		LEFT JOIN incfactory_db.kaiten_card_summaries s ON c.card_id = s.card_id
		INNER JOIN incfactory_db.kaiten_summaries_vectors v ON c.card_id = v.card_id
		WHERE (
			COALESCE(1 - (v.summary_embedding  <=> $1), 0) * $2::FLOAT +
			COALESCE(1 - (v.solution_embedding <=> $1), 0) * $3::FLOAT
		) >= $4::FLOAT
		ORDER BY similarity DESC
		LIMIT $5`

	// Преобразуем embedding в правильный формат для pgvector
	embeddingStr := float32SliceToString(embedding)
	args := []interface{}{embeddingStr, wSummary, wSolution, minSim, limit}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("ошибка поиска: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Title, &r.Summary, &r.Solution, &r.Category, &r.Similarity); err != nil {
			return nil, fmt.Errorf("ошибка чтения результата: %w", err)
		}
		results = append(results, r)
	}

	return results, nil
}
