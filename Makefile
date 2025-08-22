.PHONY: up down logs clean fetcher fetcher-date fetcher-custom health status restart pull update build dev-build dev-up dev-down

# Запуск всех сервисов
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
	docker-compose --profile fetcher run --rm fetcher

# Запуск fetcher с текущей датой
fetcher-date:
	docker-compose --profile fetcher run --rm fetcher --start-date $(shell date +%Y-%m-%d)

# Запуск fetcher с конкретной датой
fetcher-custom:
	docker-compose --profile fetcher run --rm fetcher --start-date $(DATE)

# Статус сервисов
status:
	docker-compose ps

# Перезапуск всех сервисов
restart:
	docker-compose restart

# Перезапуск конкретного сервиса
restart-assistant:
	docker-compose restart assistant

restart-llm:
	docker-compose restart llm-service

restart-processor:
	docker-compose restart processor

# Обновление образов
pull:
	docker-compose pull

# Обновление и перезапуск
update:
	docker-compose pull
	docker-compose up -d

# Health check
health:
	./scripts/health-check.sh

# Очистка
clean:
	docker-compose down --rmi all --volumes --remove-orphans

# Команды для разработки (с build)
dev-build:
	docker-compose -f docker-compose.dev.yml build

dev-up:
	docker-compose -f docker-compose.dev.yml up -d

dev-down:
	docker-compose -f docker-compose.dev.yml down

# Сборка образов (для разработки)
build: dev-build
