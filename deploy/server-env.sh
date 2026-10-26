#!/usr/bin/env bash
# Prepares .env next to docker-compose.yml on the server. The deploy workflow
# runs it before every start:
#
#   printf '%s' "$LLM_API_KEY" | bash server-env.sh REGISTRY [SITE_ADDRESS LANGFUSE_ADDRESS GRAFANA_ADDRESS LANGFUSE_URL]
#
# On the first run it creates .env from .env.example with fresh secrets, the
# same way ./run.sh does on a developer machine. On every run the values kept in
# GitHub overwrite theirs in .env: the LLM key comes on stdin (a secret, so it
# never appears in a command line or the process list), the image registry and
# the non-empty public addresses come as arguments. Everything else in .env is
# edited on the server and stays as it is.
set -euo pipefail
cd "$(dirname "$0")"

registry=${1:?usage: server-env.sh REGISTRY [SITE_ADDRESS LANGFUSE_ADDRESS GRAFANA_ADDRESS LANGFUSE_URL]}
site_address=${2:-}
langfuse_address=${3:-}
grafana_address=${4:-}
langfuse_url=${5:-}
llm_key=
IFS= read -r llm_key || true

# set_var NAME VALUE replaces the line NAME=... in .env or appends it. The value
# goes through the environment, not awk's command line.
set_var() {
	local tmp
	tmp=$(mktemp)
	NAME=$1 VALUE=$2 awk 'BEGIN { FS = "="; k = ENVIRON["NAME"]; v = ENVIRON["VALUE"] }
		$1 == k { print k "=" v; done = 1; next } { print }
		END { if (!done) print k "=" v }' .env >"$tmp"
	cat "$tmp" >.env
	rm -f "$tmp"
}

hex() { openssl rand -hex "$1"; }

if [ ! -f .env ]; then
	command -v openssl >/dev/null || {
		echo "openssl is required to generate secrets on the first deploy" >&2
		exit 1
	}
	(umask 077 && cp .env.example .env)
	for name in POSTGRES_PASSWORD CLICKHOUSE_PASSWORD REDIS_AUTH MINIO_ROOT_PASSWORD GRAFANA_ADMIN_PASSWORD \
		LANGFUSE_NEXTAUTH_SECRET LANGFUSE_SALT LANGFUSE_INIT_USER_PASSWORD; do
		set_var "$name" "$(hex 24)"
	done
	set_var LANGFUSE_ENCRYPTION_KEY "$(hex 32)"
	set_var LANGFUSE_INIT_PROJECT_PUBLIC_KEY "pk-lf-$(hex 16)"
	set_var LANGFUSE_INIT_PROJECT_SECRET_KEY "sk-lf-$(hex 16)"
	echo ".env created with fresh secrets"
fi
chmod 600 .env

set_var IMAGE_REGISTRY "$registry"
if [ -n "$llm_key" ]; then set_var LLM_API_KEY "$llm_key"; fi
if [ -n "$site_address" ]; then set_var SITE_ADDRESS "$site_address"; fi
if [ -n "$langfuse_address" ]; then set_var LANGFUSE_ADDRESS "$langfuse_address"; fi
if [ -n "$grafana_address" ]; then set_var GRAFANA_ADDRESS "$grafana_address"; fi
if [ -n "$langfuse_url" ]; then set_var LANGFUSE_URL "$langfuse_url"; fi
