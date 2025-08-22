.PHONY: build up down logs clean fetcher fetcher-date fetcher-custom health rebuild logs-assistant logs-llm logs-processor

# Сборка всех образов
build:
	docker-compose build

# Запуск постоянных сервисов
up:
	docker-compose up -d

# Остановка сервисов
down:
	docker-compose down

# Просмотр логов
logs:
	docker-compose logs -f

# Логи конкретного сервиса
logs-assistant:
	docker-compose logs -f assistant

logs-llm:
	docker-compose logs -f llm-service

logs-processor:
	docker-compose logs -f processor

# Запуск fetcher без параметров (последние 3 дня)
fetcher:
	docker-compose -f docker-compose.fetcher.yml --profile fetcher run --rm fetcher

# Запуск fetcher с текущей датой
fetcher-date:
	docker-compose -f docker-compose.fetcher.yml --profile fetcher run --rm fetcher --start-date $(shell date +%Y-%m-%d)

# Запуск fetcher с конкретной датой
fetcher-custom:
	docker-compose -f docker-compose.fetcher.yml --profile fetcher run --rm fetcher --start-date $(DATE)

# Очистка
clean:
	docker-compose down --rmi all --volumes --remove-orphans

# Health check
health:
	./scripts/health-check.sh

# Пересборка и перезапуск
rebuild:
	docker-compose down
	docker-compose build --no-cache
	docker-compose up -d

# Полный запуск
start: up

# Остановка
stop: down

# Статус сервисов
status:
	docker-compose ps

# Перезапуск конкретного сервиса
restart-assistant:
	docker-compose restart assistant

restart-llm:
	docker-compose restart llm-service

restart-processor:
	docker-compose restart processor
