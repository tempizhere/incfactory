#!/bin/sh
# entrypoint.sh - скрипт запуска сервисов с ожиданием зависимостей

set -e

echo "Starting $SERVICE service..."

# Ждем готовности внешних зависимостей в зависимости от сервиса
if [ "$SERVICE" = "assistant" ]; then
  echo "Assistant: waiting for RabbitMQ and PostgreSQL..."
  until nc -z $RABBITMQ_HOST $RABBITMQ_PORT; do
    echo "Waiting for RabbitMQ at $RABBITMQ_HOST:$RABBITMQ_PORT..."
    sleep 2
  done
  until nc -z $DB_HOST $DB_PORT; do
    echo "Waiting for PostgreSQL at $DB_HOST:$DB_PORT..."
    sleep 2
  done
  echo "Assistant: all dependencies ready"
fi

if [ "$SERVICE" = "llm-service" ]; then
  echo "LLM Service: waiting for RabbitMQ..."
  until nc -z $RABBITMQ_HOST $RABBITMQ_PORT; do
    echo "Waiting for RabbitMQ at $RABBITMQ_HOST:$RABBITMQ_PORT..."
    sleep 2
  done
  echo "LLM Service: RabbitMQ ready"
fi

if [ "$SERVICE" = "processor" ]; then
  echo "Processor: waiting for RabbitMQ and PostgreSQL..."
  until nc -z $RABBITMQ_HOST $RABBITMQ_PORT; do
    echo "Waiting for RabbitMQ at $RABBITMQ_HOST:$RABBITMQ_PORT..."
    sleep 2
  done
  until nc -z $DB_HOST $DB_PORT; do
    echo "Waiting for PostgreSQL at $DB_HOST:$DB_PORT..."
    sleep 2
  done
  echo "Processor: all dependencies ready"
fi

if [ "$SERVICE" = "fetcher" ]; then
  echo "Fetcher: waiting for RabbitMQ..."
  until nc -z $RABBITMQ_HOST $RABBITMQ_PORT; do
    echo "Waiting for RabbitMQ at $RABBITMQ_HOST:$RABBITMQ_PORT..."
    sleep 2
  done
  echo "Fetcher: RabbitMQ ready"
fi

echo "Starting $SERVICE..."
exec "$@"
