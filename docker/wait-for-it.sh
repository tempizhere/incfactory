#!/bin/sh
# wait-for-it.sh - скрипт ожидания готовности внешних зависимостей

set -e

hostport="$1"
shift
cmd="$@"

# Разделяем host и port
host="${hostport%:*}"
port="${hostport#*:}"

until nc -z "$host" "$port"; do
  echo "Waiting for $host:$port to be ready..."
  sleep 1
done

echo "$host:$port is up - executing command"
exec $cmd
