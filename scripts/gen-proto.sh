#!/usr/bin/env bash
# Regenerates Go code from every proto/ file into gen/.
# CI checks the result with `git diff --exit-code gen`, so use the same
# versions as CI (see .github/workflows/ci.yml):
#   protoc 36.2 from https://github.com/protocolbuffers/protobuf/releases
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
set -euo pipefail

cd "$(dirname "$0")/.."

rm -rf gen
mapfile -t files < <(find proto -name '*.proto' | sort)
protoc \
  --proto_path=proto \
  --go_out=. --go_opt=module=horkilion \
  --go-grpc_out=. --go-grpc_opt=module=horkilion \
  "${files[@]}"
