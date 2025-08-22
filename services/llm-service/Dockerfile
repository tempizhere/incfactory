# Многоэтапная сборка для LLM Service
FROM golang:1.23-alpine AS builder

# Устанавливаем необходимые пакеты для сборки
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Копируем файлы зависимостей
COPY go.mod ./

# Скачиваем зависимости
RUN go mod download

# Копируем исходный код
COPY . .

# Собираем приложение
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/llm-service

# Финальный образ
FROM alpine:latest

# Устанавливаем необходимые пакеты для runtime
RUN apk --no-cache add ca-certificates tzdata netcat-openbsd

WORKDIR /root/

# Копируем собранное приложение
COPY --from=builder /app/main .

# Копируем конфигурационные файлы
COPY --from=builder /app/config ./config

# Копируем скрипты
COPY --from=builder /app/docker/entrypoint.sh .
COPY --from=builder /app/docker/wait-for-it.sh .

# Делаем скрипты исполняемыми
RUN chmod +x entrypoint.sh wait-for-it.sh

# Устанавливаем entrypoint
ENTRYPOINT ["./entrypoint.sh"]
CMD ["./main"]
