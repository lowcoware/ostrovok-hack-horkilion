# О!дин запрос

Прототип AI-агента для путешествий: одно сообщение пользователя превращается в план поездки с вариантами отелей, погодой и имитированной покупкой. Задание описано в [docs/case.md](docs/case.md), архитектура — в [ADR 0001](docs/adr/0001-architecture.md).

## Что внутри

| Каталог | Что это |
|---|---|
| `api/openapi.yaml` | Внешнее API агента (`/api/v1`): сессии, сообщения, правка плана, SSE-события, заказы, `TripPlan` v2. Источник правды для фронта и любых других клиентов |
| `cmd/gateway` | HTTP API и SSE по `api/openapi.yaml`: cookie владельца, проверка владения (чужое — `404`), очередь `AGENT_TURNS`, события агента, заказ через gRPC |
| `cmd/agent` | Воркер хода агента: роутер или патч из интерфейса, `TripPlan` v2 с инвалидацией, гейты этапов, вызовы поставщиков по gRPC, стриминг ответа |
| `cmd/inventory` | gRPC-поставщики `catalog`, `lodging`, `transport`, `transfer`. Реализация каждого выбирается переменной `*_PROVIDER`: сейчас погода из Open-Meteo, синтетические отели, транспорт и трансферы — заглушки до фазы 1 |
| `cmd/booking` | gRPC: идемпотентные заказы из нескольких позиций, перепроверка цены каждой позиции у её поставщика, имитация оплаты, transactional outbox → JetStream `BOOKINGS` |
| `cmd/notifier` | Консьюмер `BOOKINGS`: уведомления (пока в лог) и событие воронки в ClickHouse |
| `cmd/migrate` | Миграции PostgreSQL (goose), запускается один раз перед сервисами |
| `proto/`, `gen/` | gRPC-контракты поставщиков и booking (`common`, `catalog`, `lodging`, `transport`, `transfer`, `booking`) и сгенерированный код (`scripts/gen-proto.sh`) |
| `config/rules.yaml` | Бизнес-правила агента: стыковки, уточнения, наценка. Неверный файл — агент не стартует |
| `web/` | React + Vite + Tailwind + shadcn/ui + Magic UI, отдаётся через Caddy |
| `docs/`, `docs-site/` | Документация проекта и сайт на Docusaurus, который её показывает по адресу `/docs/` |
| `deploy/` | docker-compose всего стека, Caddy, Prometheus, Grafana, init-скрипты баз |

Синхронные вызовы между сервисами идут по gRPC, асинхронные (ход агента, события бронирования) — через NATS JetStream. Источник правды — PostgreSQL. Трейсы LLM — Langfuse, метрики — Prometheus и Grafana, воронка — база `analytics` в ClickHouse от Langfuse.

LLM — любой OpenAI-совместимый эндпоинт: `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`, `LLM_ROUTER_MODEL`, `LLM_EXTRA_BODY` (`OPENROUTER_API_KEY` пока читается как запасное имя ключа). Без ключа агент работает на правилах и шаблонных ответах, весь путь от сообщения до покупки при этом проходится. С ключом роутер и ответы идут через модели из [docs/models.md](docs/models.md).

## Локальный запуск

Нужен Docker с compose v2. Ключи не нужны: без ключа LLM агент работает на правилах.

```bash
./run.sh        # приложение: сервисы, фронт, PostgreSQL, NATS — http://localhost, документация — http://localhost/docs/
./run.sh full   # плюс Langfuse (http://localhost:3000), ClickHouse, Prometheus, Grafana (http://localhost:3001)
./run.sh down   # остановить, данные в томах остаются
```

При первом запуске `run.sh` создаёт `deploy/.env` из `.env.example` со свежими секретами. Потом он берёт образы из GHCR, а если их не скачать (пакеты приватные, нет `docker login`), собирает их локально. Ключ LLM вписывается в `LLM_API_KEY` в `deploy/.env`, после этого `./run.sh` нужно запустить ещё раз.

Профиль задаёт `COMPOSE_PROFILES` в `deploy/.env`: пусто — только приложение, `full` — весь стек. Без профиля аналитика воронки выключена (`ANALYTICS_URL=`), трейсы тоже: ключи Langfuse пустые. Собрать всё из исходников вручную:

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.build.yml up --build -d
```

Разработка без контейнеров: Go 1.27, Node 24. `go test ./...`, `cd web && npm run dev` (проксирует `/api` на `localhost:8080`), `cd docs-site && npm start` (документация с живой перезагрузкой на http://localhost:3000/docs/).

### Контракты

Контракт меняется только PR, который правит его и код по обе стороны. CI падает, если сгенерированный код отстал:

- `api/openapi.yaml` → `cd web && npm run gen:api` обновляет `web/src/api/schema.ts`; руками типы ответов API не пишем;
- `proto/` → `scripts/gen-proto.sh` обновляет `gen/` (protoc 36.2, `protoc-gen-go` v1.36.12, `protoc-gen-go-grpc` v1.6.2);
- линт OpenAPI: `npx @redocly/cli lint api/openapi.yaml`.

Тесты сверяют с `api/openapi.yaml` ответы gateway, `TripPlan`, патч и события SSE (Go, `kin-openapi`) и фикстуру моков (`cd web && npm test`).

### Только фронтенд

Без бэкенда вообще, на моках MSW: план полной поездки (транспорт туда и обратно, отель, два трансфера, проверки стыковок) из `web/src/mocks/fixture.ts`, каждое сообщение проигрывает события `status`, `token`, `plan`, `done`:

```bash
cd web && npm ci
VITE_MOCKS=1 npm run dev
```

С настоящим бэкендом: dev-сервер проксирует `/api` на задеплоенный:

```bash
cd web && npm ci
VITE_API_TARGET=https://<домен приложения> npm run dev
```

## Как вносить изменения

Одна ветка `main`, она же то, что работает на сервере. Каждая задача — короткая ветка и PR в `main`, отдельных веток под фронт и бэк нет. CI смотрит, что поменялось: PR только в `web/` проверяет только сборку фронта, PR в Go-коде — только Go. После мержа деплой пересобирает лишь изменённую часть: правка фронта собирает и перезапускает один контейнер `web`, бэкенд не трогается.

### Журнал изменений

Каждое изменение записывается в [CHANGELOG.md](CHANGELOG.md) строго по [Keep a Changelog 1.1.0](https://keepachangelog.com/ru/1.1.0/): раздел `[Unreleased]`, разделы «Добавлено», «Изменено», «Устарело», «Удалено», «Исправлено», «Безопасность». В каждом пункте — суть, технические детали после «Технически:» и короткий хеш коммита, например (`29776b5`). Коммит не может содержать свой хеш, поэтому порядок такой: коммит с кодом, затем запись с его хешем отдельным коммитом. Проверка: `python3 scripts/check-changelog.py`, в CI она идёт на каждом PR. PR с записями в журнале мержится через merge commit: squash и rebase меняют хеши, и записи станут ложными.

## Деплой через GitHub Actions

На каждый push в `main` workflow `Deploy` прогоняет CI, собирает изменившиеся образы в GHCR (`ghcr.io/<owner>/horkilion-*:<sha>`), копирует `deploy/` на сервер по SSH и делает `docker compose pull && up -d`. Go-сервисы и фронт живут под разными тегами, текущие теги сервер хранит в `~/horkilion/.image-tags`. Деплой считается успешным, когда `gateway` проходит health-проверку. На сервере в `deploy/.env` должно стоять `COMPOSE_PROFILES=full`, иначе Langfuse и Grafana не поднимутся.

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
- уведомления пишутся в лог, на сайте их пока нет;
- в Langfuse трейсится только `agent`; вызовы `inventory` и `booking` видны как наблюдения внутри хода агента, без собственных спанов.

## Трейсы в Langfuse

Каждый ход агента — отдельный трейс `agent-turn`, сгруппированный по сессии чата. Внутри видны роутер (вызов LLM или правила), поиск отелей и погода (`tool`) и ответ. Вызовы LLM записываются как `generation` с моделью и токенами. Агент отправляет трейсы через OpenTelemetry (OTLP/HTTP) на `/api/public/otel` с ключами проекта из `LANGFUSE_INIT_PROJECT_*`. Langfuse создаёт этот проект и пользователя для входа при старте.
