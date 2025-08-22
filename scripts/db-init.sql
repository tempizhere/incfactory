-- Создаем базу данных kaiten_test если её нет
SELECT 'CREATE DATABASE kaiten_test'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'kaiten_test')\gexec

-- Подключаемся к базе kaiten_test и применяем миграции
\c kaiten_test

-- Создаём схему incfactory_db
CREATE SCHEMA IF NOT EXISTS incfactory_db;

-- Включаем расширение pgvector для работы с векторными данными
CREATE EXTENSION IF NOT EXISTS vector;

-- Создаём таблицу kaiten_cards для хранения карточек
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_cards (
    card_id VARCHAR(100) PRIMARY KEY,
    title VARCHAR(500),
    description TEXT,
    board_id VARCHAR(100),
    column_id VARCHAR(100),
    space_id VARCHAR(100),
    parents INTEGER[],
    children INTEGER[],
    created TIMESTAMP NOT NULL,
    updated TIMESTAMP NOT NULL
);

-- Создаём индексы для kaiten_cards
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_space_id ON incfactory_db.kaiten_cards (space_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_column_id ON incfactory_db.kaiten_cards (column_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_board_id ON incfactory_db.kaiten_cards (board_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_created ON incfactory_db.kaiten_cards (created);
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_updated ON incfactory_db.kaiten_cards (updated);

-- Создаём таблицу kaiten_comments для хранения комментариев
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_comments (
    comment_id VARCHAR(100) PRIMARY KEY,
    card_id VARCHAR(100) NOT NULL,
    text TEXT,
    author VARCHAR(100),
    created TIMESTAMP NOT NULL,
    updated TIMESTAMP NOT NULL,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индексы для kaiten_comments
CREATE INDEX IF NOT EXISTS idx_kaiten_comments_card_id ON incfactory_db.kaiten_comments (card_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_comments_created ON incfactory_db.kaiten_comments (created);
CREATE INDEX IF NOT EXISTS idx_kaiten_comments_author ON incfactory_db.kaiten_comments (author);

-- Создаём таблицу kaiten_card_summaries для хранения суммарий карточек
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_card_summaries (
    card_id VARCHAR(100) PRIMARY KEY,
    summary TEXT,
    solution TEXT,
    category VARCHAR(200),
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индексы для kaiten_card_summaries
CREATE INDEX IF NOT EXISTS idx_kaiten_card_summaries_card_id ON incfactory_db.kaiten_card_summaries (card_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_card_summaries_category ON incfactory_db.kaiten_card_summaries (category);
CREATE INDEX IF NOT EXISTS idx_kaiten_card_summaries_created ON incfactory_db.kaiten_card_summaries (created);

-- Создаём таблицу kaiten_summaries_vectors для хранения эмбеддингов суммарий
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_summaries_vectors (
    id SERIAL PRIMARY KEY,
    card_id VARCHAR(100) NOT NULL UNIQUE,
    summary_embedding VECTOR(1024),
    solution_embedding VECTOR(1024),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индексы для kaiten_summaries_vectors
-- HNSW индексы для быстрого поиска по косинусному расстоянию
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_summary_hnsw ON incfactory_db.kaiten_summaries_vectors
USING hnsw (summary_embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_solution_hnsw ON incfactory_db.kaiten_summaries_vectors
USING hnsw (solution_embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);

-- Дополнительные индексы для оптимизации
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_card_id ON incfactory_db.kaiten_summaries_vectors (card_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_created_at ON incfactory_db.kaiten_summaries_vectors (created_at);

-- Создаём представление для удобного поиска похожих карточек
CREATE OR REPLACE VIEW incfactory_db.similar_cards_view AS
SELECT
    c.card_id,
    c.title,
    c.space_id,
    s.summary,
    s.solution,
    s.category,
    v.summary_embedding,
    v.solution_embedding
FROM incfactory_db.kaiten_cards c
LEFT JOIN incfactory_db.kaiten_card_summaries s ON c.card_id = s.card_id
INNER JOIN incfactory_db.kaiten_summaries_vectors v ON c.card_id = v.card_id;
