#!/usr/bin/env bash
set -euo pipefail

bootstrap="${KAFKA_BOOTSTRAP:-kafka:19092}"
topics="/opt/kafka/bin/kafka-topics.sh"

for _ in $(seq 1 30); do
  if "$topics" --bootstrap-server "$bootstrap" --list >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

"$topics" --bootstrap-server "$bootstrap" --list >/dev/null

create_topic() {
  local topic="$1"
  local retention_ms="$2"
  local segment_ms="$3"

  "$topics" \
    --bootstrap-server "$bootstrap" \
    --create \
    --if-not-exists \
    --topic "$topic" \
    --partitions 3 \
    --replication-factor 1 \
    --config "retention.ms=${retention_ms}" \
    --config "segment.ms=${segment_ms}" \
    --config "cleanup.policy=delete"
}

seven_days_ms=$((7 * 24 * 60 * 60 * 1000))
one_day_ms=$((24 * 60 * 60 * 1000))

create_topic "rides.requested" "$seven_days_ms" "$one_day_ms"
create_topic "rides.assigned" "$seven_days_ms" "$one_day_ms"
create_topic "rides.released" "$seven_days_ms" "$one_day_ms"
create_topic "rides.matched" "$seven_days_ms" "$one_day_ms"
create_topic "rides.match-failed" "$seven_days_ms" "$one_day_ms"

# segment.ms curto: sem rolagem de segmento a retenção não apaga nada.
create_topic "drivers.location" "$((5 * 60 * 1000))" "$((60 * 1000))"
create_topic "drivers.presence" "$((60 * 60 * 1000))" "$((5 * 60 * 1000))"

"$topics" --bootstrap-server "$bootstrap" --describe
