#!/bin/bash
set -e

# Создаем базу данных kaiten_test если её нет
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    SELECT 'CREATE DATABASE kaiten_test'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'kaiten_test')\gexec
EOSQL

# Применяем миграции к базе kaiten_test
echo "Применяем миграции к базе kaiten_test..."
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "kaiten_test" -f /docker-entrypoint-initdb.d/01-migrations.sql

echo "База данных kaiten_test создана и миграции применены!"
