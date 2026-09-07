#!/usr/bin/env bash
# Starts or stops a Cloud Spanner emulator in Docker for the emulator-gated tests.
#
#   ./scripts/spanner-emulator.sh start   # then: make test-emulator
#   ./scripts/spanner-emulator.sh stop
set -euo pipefail

IMAGE="${SPANNER_EMULATOR_IMAGE:-gcr.io/cloud-spanner-emulator/emulator:1.5.57}"
NAME="${SPANNER_EMULATOR_CONTAINER:-adk-sessions-spanner-emulator}"
GRPC_PORT="${SPANNER_EMULATOR_GRPC_PORT:-9010}"
REST_PORT="${SPANNER_EMULATOR_REST_PORT:-9020}"

case "${1:-start}" in
  start)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker run -d --name "$NAME" -p "${GRPC_PORT}:9010" -p "${REST_PORT}:9020" "$IMAGE" >/dev/null
    # The emulator answers gRPC before it is fully ready; give it a moment.
    for _ in $(seq 1 30); do
      if docker logs "$NAME" 2>&1 | grep -q "gRPC server listening"; then
        break
      fi
      sleep 1
    done
    echo "export SPANNER_EMULATOR_HOST=localhost:${GRPC_PORT}"
    ;;
  stop)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    ;;
  *)
    echo "usage: $0 [start|stop]" >&2
    exit 2
    ;;
esac
