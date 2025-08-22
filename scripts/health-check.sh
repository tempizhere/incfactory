#!/bin/bash
# health-check.sh - скрипт проверки здоровья всех сервисов

set -e

echo "🔍 Проверка здоровья сервисов IncFactory..."

# Цвета для вывода
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Функция для проверки статуса
check_status() {
    local service=$1
    local status=$(docker-compose ps -q $service 2>/dev/null | wc -l)

    if [ $status -eq 1 ]; then
        local container_status=$(docker-compose ps $service | tail -n 1 | awk '{print $4}')
        if [[ $container_status == *"Up"* ]]; then
            echo -e "${GREEN}✅ $service: запущен${NC}"
            return 0
        else
            echo -e "${RED}❌ $service: не запущен (статус: $container_status)${NC}"
            return 1
        fi
    else
        echo -e "${RED}❌ $service: не найден${NC}"
        return 1
    fi
}

# Функция для проверки health check
check_health() {
    local service=$1
    local url=$2

    if curl -f -s $url > /dev/null 2>&1; then
        echo -e "${GREEN}✅ $service health check: OK${NC}"
        return 0
    else
        echo -e "${RED}❌ $service health check: FAILED${NC}"
        return 1
    fi
}

# Проверяем статус всех сервисов
echo "📊 Статус контейнеров:"
check_status "assistant"
check_status "llm-service"
check_status "processor"

echo ""
echo "🏥 Health checks:"

# Проверяем health check assistant
if check_health "assistant" "http://localhost:8080/health"; then
    assistant_health=0
else
    assistant_health=1
fi

# Проверяем подключение к внешним зависимостям
echo ""
echo "🔗 Проверка внешних зависимостей:"

# Проверяем RabbitMQ
if nc -z ${RABBITMQ_HOST:-localhost} ${RABBITMQ_PORT:-5672} 2>/dev/null; then
    echo -e "${GREEN}✅ RabbitMQ: доступен${NC}"
    rabbitmq_health=0
else
    echo -e "${RED}❌ RabbitMQ: недоступен${NC}"
    rabbitmq_health=1
fi

# Проверяем PostgreSQL
if nc -z ${DB_HOST:-localhost} ${DB_PORT:-5432} 2>/dev/null; then
    echo -e "${GREEN}✅ PostgreSQL: доступен${NC}"
    postgres_health=0
else
    echo -e "${RED}❌ PostgreSQL: недоступен${NC}"
    postgres_health=1
fi

# Итоговая оценка
echo ""
echo "📈 Итоговая оценка:"

total_health=$((assistant_health + rabbitmq_health + postgres_health))

if [ $total_health -eq 0 ]; then
    echo -e "${GREEN}🎉 Все сервисы работают корректно!${NC}"
    exit 0
elif [ $total_health -eq 1 ]; then
    echo -e "${YELLOW}⚠️  Есть незначительные проблемы${NC}"
    exit 1
else
    echo -e "${RED}🚨 Критические проблемы! Проверьте логи: make logs${NC}"
    exit 2
fi
