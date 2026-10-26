#!/usr/bin/env bash
# Starts the whole application from registry images on a machine with Docker.
#
#   ./run.sh         the application: services, frontend, PostgreSQL, NATS
#   ./run.sh full    plus Langfuse, ClickHouse, Prometheus and Grafana
#   ./run.sh down    stops the stack, data volumes stay
#
# On the first run it writes deploy/.env from deploy/.env.example with fresh
# secrets. No LLM key is needed: without one the agent runs on rules. Put a key
# into LLM_API_KEY in deploy/.env and run again to use the models.
set -euo pipefail

cd "$(dirname "$0")/deploy"
mode=${1:-app}
case "$mode" in
app | full | down) ;;
*)
	echo "usage: ./run.sh [full|down]" >&2
	exit 2
	;;
esac

command -v docker >/dev/null || {
	echo "Docker with compose v2 is required" >&2
	exit 1
}

if [ "$mode" = down ]; then
	docker compose down
	exit 0
fi

hex() { openssl rand -hex "$1"; }

# set_var NAME VALUE replaces the line NAME=... in .env, or appends it to an
# .env written before NAME existed.
set_var() {
	local tmp
	tmp=$(mktemp)
	awk -v k="$1" -v v="$2" 'BEGIN { FS = "=" } $1 == k { print k "=" v; done = 1; next } { print } END { if (!done) print k "=" v }' .env >"$tmp"
	cat "$tmp" >.env
	rm -f "$tmp"
}

get_var() { sed -n "s/^$1=//p" .env; }

if [ ! -f .env ]; then
	cp .env.example .env
	chmod 600 .env
	for name in POSTGRES_PASSWORD CLICKHOUSE_PASSWORD REDIS_AUTH MINIO_ROOT_PASSWORD GRAFANA_ADMIN_PASSWORD \
		LANGFUSE_NEXTAUTH_SECRET LANGFUSE_SALT LANGFUSE_INIT_USER_PASSWORD; do
		set_var "$name" "$(hex 24)"
	done
	set_var LANGFUSE_ENCRYPTION_KEY "$(hex 32)"
	# Images CI publishes for this repository; override with IMAGE_REGISTRY=... ./run.sh.
	set_var IMAGE_REGISTRY "${IMAGE_REGISTRY:-ghcr.io/lowcoware}"
	echo "deploy/.env created with fresh secrets"
fi

if [ "$mode" = full ]; then
	set_var COMPOSE_PROFILES full
	set_var ANALYTICS_URL http://clickhouse:8123
	if [ -z "$(get_var LANGFUSE_INIT_PROJECT_PUBLIC_KEY)" ]; then
		set_var LANGFUSE_INIT_PROJECT_PUBLIC_KEY "pk-lf-$(hex 16)"
		set_var LANGFUSE_INIT_PROJECT_SECRET_KEY "sk-lf-$(hex 16)"
	fi
else
	set_var COMPOSE_PROFILES ""
	set_var ANALYTICS_URL ""
	# impulse: traces keep their Langfuse keys after a switch back from full and fail to send, clear them here if the log noise matters.
fi

# Registry images when they can be pulled, a local build otherwise (private
# packages without docker login, or no image published yet).
if docker compose pull --quiet; then
	docker compose up -d --remove-orphans
else
	echo "pulling images failed, building them locally"
	docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build --remove-orphans
fi

echo "waiting for the gateway..."
for _ in $(seq 1 60); do
	if docker compose exec -T gateway /app healthcheck >/dev/null 2>&1; then
		echo "ready: http://localhost"
		if [ "$mode" = full ]; then
			echo "Langfuse: http://localhost:3000 ($(get_var LANGFUSE_INIT_USER_EMAIL), password in deploy/.env)"
			echo "Grafana:  http://localhost:3001 (admin, GRAFANA_ADMIN_PASSWORD in deploy/.env)"
		fi
		exit 0
	fi
	sleep 5
done
docker compose ps
docker compose logs --tail=50 gateway agent inventory migrate
exit 1
