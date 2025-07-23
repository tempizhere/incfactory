-- Создаём схему incfactory_db
CREATE SCHEMA IF NOT EXISTS incfactory_db;

-- Включаем расширение pgvector
CREATE EXTENSION IF NOT EXISTS vector;

-- Создаём таблицу kaiten_cards для хранения карточек
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_cards (
    card_id VARCHAR(50) PRIMARY KEY,
    title VARCHAR(255),
    description TEXT,
    board_id VARCHAR(50),
    column_id VARCHAR(50),
    space_id VARCHAR(50),
    parents INTEGER[],
    children INTEGER[],
    created TIMESTAMP NOT NULL,
    updated TIMESTAMP NOT NULL
);

-- Создаём индексы для kaiten_cards
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_space_id ON incfactory_db.kaiten_cards (space_id);
CREATE INDEX IF NOT EXISTS idx_kaiten_cards_column_id ON incfactory_db.kaiten_cards (column_id);

-- Создаём таблицу kaiten_comments для хранения комментариев
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_comments (
    comment_id VARCHAR(50) PRIMARY KEY,
    card_id VARCHAR(50) NOT NULL,
    text TEXT,
    author VARCHAR(50),
    created TIMESTAMP NOT NULL,
    updated TIMESTAMP NOT NULL,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индекс для kaiten_comments
CREATE INDEX IF NOT EXISTS idx_kaiten_comments_card_id ON incfactory_db.kaiten_comments (card_id);

-- Создаём таблицу kaiten_card_summaries для хранения суммарий карточек
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_card_summaries (
    card_id VARCHAR(50) PRIMARY KEY,
    summary TEXT,
    solution TEXT,
    category VARCHAR(100),
    created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индекс для kaiten_card_summaries
CREATE INDEX IF NOT EXISTS idx_kaiten_card_summaries_card_id ON incfactory_db.kaiten_card_summaries (card_id);

-- Создаём таблицу kaiten_summaries_vectors для хранения эмбеддингов суммарий
CREATE TABLE IF NOT EXISTS incfactory_db.kaiten_summaries_vectors (
    id SERIAL PRIMARY KEY,
    card_id VARCHAR(50) NOT NULL UNIQUE,
    summary_embedding VECTOR(1024),
    solution_embedding VECTOR(1024),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (card_id) REFERENCES incfactory_db.kaiten_cards (card_id) ON DELETE CASCADE
);

-- Создаём индексы для kaiten_summaries_vectors
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_embedding_hnsw ON incfactory_db.kaiten_summaries_vectors
USING hnsw (summary_embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS idx_kaiten_summaries_vectors_solution_embedding_hnsw ON incfactory_db.kaiten_summaries_vectors
USING hnsw (solution_embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);