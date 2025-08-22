#!/bin/sh
# wait-for-it.sh - скрипт ожидания готовности внешних зависимостей

set -e

host="$1"
shift
cmd="$@"

until nc -z "$host" "${host#*:}"; do
  echo "Waiting for $host to be ready..."
  sleep 1
done

echo "$host is up - executing command"
exec $cmd
