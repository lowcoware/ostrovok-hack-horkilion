#!/usr/bin/env bash
# Regenerates Go code from proto/ into gen/.
# Requires protoc, protoc-gen-go and protoc-gen-go-grpc on PATH:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

cd "$(dirname "$0")/.."

protoc \
  --proto_path=proto \
  --go_out=. --go_opt=module=horkilion \
  --go-grpc_out=. --go-grpc_opt=module=horkilion \
  proto/inventory/v1/inventory.proto \
  proto/booking/v1/booking.proto
