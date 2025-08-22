#!/bin/sh
# entrypoint.sh - скрипт запуска сервисов с ожиданием зависимостей

set -e

echo "Starting $SERVICE service..."

# Ждем готовности внешних зависимостей в зависимости от сервиса
if [ "$SERVICE" = "assistant" ]; then
  echo "Assistant: waiting for RabbitMQ and PostgreSQL..."
  ./wait-for-it.sh $RABBITMQ_HOST:$RABBITMQ_PORT
  ./wait-for-it.sh $DB_HOST:$DB_PORT
  echo "Assistant: all dependencies ready"
fi

if [ "$SERVICE" = "llm-service" ]; then
  echo "LLM Service: waiting for RabbitMQ..."
  ./wait-for-it.sh $RABBITMQ_HOST:$RABBITMQ_PORT
  echo "LLM Service: RabbitMQ ready"
fi

if [ "$SERVICE" = "processor" ]; then
  echo "Processor: waiting for RabbitMQ and PostgreSQL..."
  ./wait-for-it.sh $RABBITMQ_HOST:$RABBITMQ_PORT
  ./wait-for-it.sh $DB_HOST:$DB_PORT
  echo "Processor: all dependencies ready"
fi

if [ "$SERVICE" = "fetcher" ]; then
  echo "Fetcher: waiting for RabbitMQ..."
  ./wait-for-it.sh $RABBITMQ_HOST:$RABBITMQ_PORT
  echo "Fetcher: RabbitMQ ready"
fi

echo "Starting $SERVICE..."
exec "$@"
