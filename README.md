# О!дин запрос

Прототип AI-агента для путешествий: одно сообщение пользователя превращается в план поездки с вариантами отелей, погодой и имитированной покупкой. Задание описано в [docs/case.md](docs/case.md), архитектура — в [ADR 0001](docs/adr/0001-architecture.md).

## Что внутри

| Каталог | Что это |
|---|---|
| `cmd/gateway` | HTTP API и SSE для фронта. Кладёт сообщения в очередь `AGENT_TURNS`, транслирует события агента, оформляет заказ через gRPC |
| `cmd/agent` | Воркер хода агента: роутер, патч `TripPlan` с инвалидацией, гейты этапов, вызовы `inventory` по gRPC, стриминг ответа |
| `cmd/inventory` | gRPC: синтетические отели (детерминированные), цены, погода из Open-Meteo |
| `cmd/booking` | gRPC: идемпотентные заказы, имитация оплаты, transactional outbox → JetStream `BOOKINGS` |
| `cmd/notifier` | Консьюмер `BOOKINGS`: уведомления (пока в лог) и событие воронки в ClickHouse |
| `cmd/migrate` | Миграции PostgreSQL (goose), запускается один раз перед сервисами |
| `proto/`, `gen/` | gRPC-контракты и сгенерированный код (`scripts/gen-proto.sh`) |
| `web/` | React + Vite + Tailwind + shadcn/ui + Magic UI, отдаётся через Caddy |
| `deploy/` | docker-compose всего стека, Caddy, Prometheus, Grafana, init-скрипты баз |

Синхронные вызовы между сервисами идут по gRPC, асинхронные (ход агента, события бронирования) — через NATS JetStream. Источник правды — PostgreSQL. Трейсы LLM — Langfuse, метрики — Prometheus и Grafana, воронка — база `analytics` в ClickHouse от Langfuse.

Без `OPENROUTER_API_KEY` агент работает на правилах и шаблонных ответах, весь путь от сообщения до покупки при этом проходится. С ключом роутер и ответы идут через модели из [docs/models.md](docs/models.md).

## Локальный запуск

Нужен Docker с compose v2.

```bash
cp deploy/.env.example deploy/.env   # заполнить секреты: openssl rand -hex 24
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.build.yml up --build -d
```

- приложение: http://localhost
- Langfuse: http://localhost:3000
- Grafana: http://localhost:3001 (admin / `GRAFANA_ADMIN_PASSWORD`)

Разработка без контейнеров: Go 1.27, Node 24. `go test ./...`, `cd web && npm run dev` (проксирует `/api` на `localhost:8080`).

### Только фронтенд

Бэкенд локально поднимать не нужно, dev-сервер проксирует `/api` на задеплоенный:

```bash
cd web && npm ci
VITE_API_TARGET=https://<домен приложения> npm run dev
```

## Как вносить изменения

Одна ветка `main`, она же то, что работает на сервере. Каждая задача — короткая ветка и PR в `main`, отдельных веток под фронт и бэк нет. CI смотрит, что поменялось: PR только в `web/` проверяет только сборку фронта, PR в Go-коде — только Go. После мержа деплой пересобирает лишь изменённую часть: правка фронта собирает и перезапускает один контейнер `web`, бэкенд не трогается.

## Деплой через GitHub Actions

На каждый push в `main` workflow `Deploy` прогоняет CI, собирает изменившиеся образы в GHCR (`ghcr.io/<owner>/horkilion-*:<sha>`), копирует `deploy/` на сервер по SSH и делает `docker compose pull && up -d`. Go-сервисы и фронт живут под разными тегами, текущие теги сервер хранит в `~/horkilion/.image-tags`. Деплой считается успешным, когда `gateway` проходит health-проверку.

### Сервер (один раз)

1. Linux с Docker и compose-плагином. Весь стек в простое занимает около 2,3 ГБ RAM, из них 85% приходится на Langfuse с ClickHouse, сами сервисы около 100 МБ. С запасом на пики ClickHouse берите 4 ГБ минимум, комфортно 8 ГБ. Образы собираются в CI, на сервере сборки нет. Открыть порты 80 и 443, для доступа по портам ещё 3000 и 3001.
2. Пользователь для деплоя в группе `docker`, вход по SSH-ключу.
3. Создать `~/horkilion/.env` из [`deploy/.env.example`](deploy/.env.example). Для HTTPS указать домены в `SITE_ADDRESS`, `LANGFUSE_ADDRESS`, `GRAFANA_ADDRESS` (A-записи должны смотреть на сервер), Caddy сам получит сертификаты. `IMAGE_REGISTRY=ghcr.io/<owner в нижнем регистре>`.

### Настройки репозитория

Settings → Environments → создать `production` и добавить секреты:

| Секрет | Значение |
|---|---|
| `SSH_HOST` | адрес сервера |
| `SSH_USER` | пользователь для деплоя |
| `SSH_PORT` | порт SSH, если не 22 |
| `SSH_PRIVATE_KEY` | приватный ключ, публичная часть в `~/.ssh/authorized_keys` на сервере |
| `SSH_KNOWN_HOSTS` | вывод `ssh-keyscan -p <порт> <хост>` |

Необязательная переменная `DEPLOY_PATH` (Settings → Variables) задаёт каталог на сервере, по умолчанию `horkilion` в домашнем каталоге.

Workflow логинит сервер в GHCR токеном самого запуска и разлогинивает в конце. Чтобы вручную делать `docker compose pull` на сервере, нужен свой `docker login ghcr.io` с PAT (scope `read:packages`), либо пакеты надо сделать публичными.

## Что осознанно упрощено

Упрощения помечены в коде комментариями `impulse:` с условием, когда их пора убирать. Главные:

- отели синтетические и генерируются из seed, без точек OSM;
- оплата имитирована, заказ сразу `paid`;
- ходы агента обрабатываются строго по одному (`MaxAckPending: 1`) вместо лока на сессию;
- роутер без LLM работает на регулярках и не знает падежей;
- уведомления пишутся в лог, Telegram-бота пока нет;
- в Langfuse трейсится только `agent`; вызовы `inventory` и `booking` видны как наблюдения внутри хода агента, без собственных спанов.

## Трейсы в Langfuse

Каждый ход агента — отдельный трейс `agent-turn`, сгруппированный по сессии чата. Внутри видны роутер (вызов LLM или правила), поиск отелей и погода (`tool`) и ответ. Вызовы LLM записываются как `generation` с моделью и токенами. Агент отправляет трейсы через OpenTelemetry (OTLP/HTTP) на `/api/public/otel` с ключами проекта из `LANGFUSE_INIT_PROJECT_*`. Langfuse создаёт этот проект и пользователя для входа при старте.
