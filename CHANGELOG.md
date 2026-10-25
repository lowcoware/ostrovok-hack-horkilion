# Журнал изменений

Все заметные изменения проекта записываются в этот файл.

Формат основан на [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
проект придерживается [Семантического версионирования](https://semver.org/lang/ru/spec/v2.0.0.html).

## [Unreleased]

### Добавлено

- Прототип агента путешествий: чат, план поездки с вариантами отелей, погода и имитированная покупка.
  Технически: Go-сервисы `gateway`, `agent`, `inventory`, `booking`, `notifier`, `migrate`; синхронные вызовы
  по gRPC, ход агента и события бронирования через NATS JetStream (`AGENT_TURNS`, `BOOKINGS`), transactional
  outbox в `booking`; PostgreSQL, Langfuse, Prometheus, Grafana, ClickHouse; фронт React + Vite + Tailwind за
  Caddy; полный стек в `deploy/docker-compose.yml`. (`fccf943`)
- CI и деплой пересобирают только изменённую часть монорепозитория. Технически: задача `changes` в
  `.github/workflows/ci.yml` выдаёт `backend`, `web`, `deploy`; Go-сервисы и фронт деплоятся под разными
  тегами (`BACKEND_TAG`, `WEB_TAG`, файл `.image-tags` на сервере); `VITE_API_TARGET` направляет dev-сервер
  фронта на задеплоенный бэкенд. (`36e5389`)
- Документы проекта: ADR 0002 о переиспользуемости (адаптеры поставщиков, агент как API, аккаунты, запуск для
  жюри), roadmap по фазам, спецификация продукта и спека фазы 0. Технически: `docs/adr/0002-reusable-agent-platform.md`,
  `docs/roadmap.md`, `docs/spec.md`, `docs/specs/0001-phase0-contracts.md`. (`825e2da`, `634c74f`, `2afd094`)
- Внешнее API агента с версией в пути: сессии и список поездок владельца, сообщения, правка плана из
  интерфейса, события хода по SSE, заказ из выбранных вариантов, заказы, «Откуда данные», аккаунт.
  Технически: `api/openapi.yaml` (OpenAPI 3.1, префикс `/api/v1`), схемы `TripPlan` v2, `PlanPatch`,
  `AgentEvent` (`status`, `token`, `plan`, `clarify`, `done`, `error`), ошибки `{"error": {"code", "message"}}`;
  `/auth/register`, `/auth/login`, `/auth/logout` пока отвечают `501`; пакет `api` встраивает спеку для тестов;
  линт — `redocly lint`, правила в `redocly.yaml`. (`29776b5`)
- План поездки v2 из компонентов: транспорт туда и обратно, проживание, два трансфера; у варианта цена,
  объяснение, флаги и источники; проверки стыковок, конфликты, итог и превышение бюджета. Технически:
  пакет `internal/agent/plan`, таблица инвалидации из ADR 0001 в `Apply`; выбор варианта и правки карточек
  идут через `POST /api/v1/sessions/{id}/plan` тем же механизмом, что и сообщения. (`29776b5`, `5e6b0c4`)
- Контракты поставщиков v2 и поле происхождения данных `sources` у всех сущностей. Технически:
  `proto/common/v1` (`Source`, `GeoPoint`, `Place`, `Travelers` с возрастом детей), `catalog/v1`, `lodging/v1`,
  `transport/v1`, `transfer/v1`; `booking/v1` с позициями заказа и деталью ошибки `PriceChanged`;
  генерация всех файлов `proto/` скриптом `scripts/gen-proto.sh` (protoc 36.2). (`29776b5`)
- Выбор реализации поставщика в конфиге: неизвестное значение останавливает процесс со списком допустимых.
  Технически: `CATALOG_PROVIDER` (`open-data`), `LODGING_PROVIDER` (`synthetic`), `TRANSPORT_PROVIDER` и
  `TRANSFER_PROVIDER` (`stub`, отвечают `UNIMPLEMENTED` до фазы 1); `platform.Provider`; все четыре сервиса
  размещает процесс `inventory`. (`29776b5`, `f5ee3a4`)
- Заказ из нескольких позиций с перепроверкой цены у поставщика: при расхождении заказ не создаётся.
  Технически: `booking.CreateOrder` вызывает `GetOffer` поставщика каждой позиции, при расхождении —
  `FAILED_PRECONDITION` с `PriceChanged`; gateway отвечает `409` с кодом `price_changed` и новыми ценами
  или `offer_unavailable`; `ListOrders`; миграция `00002_owners_and_order_items.sql`. (`29776b5`)
- Владелец данных по анонимной cookie, чужие сессии и заказы отвечают `404`. Технически: cookie
  `horkilion_owner` (`HttpOnly`, `SameSite=Lax`), таблица `gateway.sessions`, владелец передаётся сервисам в
  метаданных gRPC `x-owner-id`; gateway подключается к PostgreSQL. (`29776b5`, `f5ee3a4`)
- Бизнес-правила агента в конфиге: время стыковок, число уточнений за ход, наценка. Технически:
  `config/rules.yaml` (путь в `RULES_PATH`), неверный файл или неизвестный ключ — ошибка запуска;
  файл копируется в образы. (`29776b5`)
- Фронт работает без бэкенда на моках: план полной поездки и проигрывание событий хода. Технически:
  `VITE_MOCKS=1 npm run dev`, MSW 3 с плагином `msw/vite`, фикстура `web/src/mocks/fixture.ts`,
  обработчики `web/src/mocks/handlers.ts`; в production-сборку моки не попадают. (`5e6b0c4`)
- Проверки соответствия контракту в тестах и CI. Технически: ответы gateway, `TripPlan`, `PlanPatch` и
  события SSE сверяются с `api/openapi.yaml` через `kin-openapi` (`internal/apitest`); фикстура моков — через
  ajv (`npm test`); CI падает, если `gen/` или `web/src/api/schema.ts` не перегенерированы после правки
  контракта. (`29776b5`, `5e6b0c4`, `f5ee3a4`)
- Журнал изменений по Keep a Changelog 1.1.0 с хешем коммита в каждой записи и его проверка. Технически:
  `CHANGELOG.md`, `scripts/check-changelog.py` (разделы и их порядок, заголовки версий, ссылки, «Технически:»
  и хеш в каждом пункте, существование хеша в репозитории); задача `changelog` в CI только на PR; правила —
  в README, PR с записями мержится через merge commit. (`9ef35b5`)
- Решение о визах по гражданству и план фазы 1 по ролям. Технически: ADR 0003 (статус proposed) — подбор по
  всем странам с пометкой визы, виза по гражданству, источник `imorte/passport-index-data` (MIT, обновлён
  2026-02-17); спека 0002 — изменения контракта: `Constraints.citizenship`, `Preferences.visa_free_only`,
  `VisaRequirement`, `GET /countries`, `GET /places`, RPC `catalog.v1.GetVisaRequirement`, `ListCountries`,
  `SearchPlaces`, проверки `visa` и `stay_within_visa_free_days`; спека 0003 — задачи `P1-BE-01…13`,
  `P1-DS-01…08`, `P1-FE-01…10` и точки стыковки CFC-1…5, включая формат снапшота данных `data/`;
  попало в `main` через PR #2. (`9fa52c0`)
- Задачи фазы 1 по ролям с подзадачами и зависимостями, чтобы бэкенд, данные и фронт работали асинхронно.
  Технически: `docs/phase1/README.md` (подзадача выполнена после мержа в `main`; зависимости блокирующие и «на
  подключение»; единственная межролевая блокирующая — `P1-FE-01.1–01.2` ждут `P1-BE-01`), `docs/phase1/backend.md`
  (`P1-BE-01…13`), `docs/phase1/data.md` (`P1-DS-01…08`), `docs/phase1/frontend.md` (`P1-FE-01…11`); спека 0003
  ссылается на эти файлы вместо своего списка задач; попало в `main` через PR #3. (`12ebf46`)

### Изменено

- План проекта перестроен под три роли и новую географию. Технически: `docs/roadmap.md` и `docs/spec.md` —
  роли бэкенд, данные и фронт вместо треков A, B, F; этап 1.0 «контракт виз» в фазе 1; визы в фазах 2 и 3;
  открытые вопросы Q-005…Q-007; датированные дополнения в ADR 0001, ADR 0002 и спеке 0001. (`9fa52c0`)

- Агент, фронт и gateway перешли на контракт фазы 0: план v2, события в конверте и API `/api/v1`.
  Технически: событие SSE — `{"type", "session_id", "turn_id", "seq", "data"}`, `turn_id` равен `message_id`
  из ответа `202`; TS-типы генерируются `npm run gen:api` (openapi-typescript 7.13 на TypeScript 5.9 через
  `npx`, в проекте TypeScript 7). (`29776b5`, `5e6b0c4`)
- LLM-провайдер настраивается переменными для любого OpenAI-совместимого эндпоинта. Технически:
  `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`, `LLM_ROUTER_MODEL`, `LLM_EXTRA_BODY` (JSON, по умолчанию
  `{"provider":{"data_collection":"deny"}}`); неверный JSON — ошибка запуска. (`29776b5`, `f5ee3a4`)
- Синтетические отели учитывают возраст детей, питание передаётся кодами, у отеля место, время заезда и
  условия отмены. Технически: младенцы до 2 лет бесплатно, дети до 12 лет — половина цены; `meal` —
  `room_only`, `breakfast`, `half_board`, `all_inclusive`; фильтры поиска в `lodging.v1.Filters`. (`29776b5`)
- Мессенджеры исключены из объёма, мобильная версия — адаптивный сайт, уведомления показываются на сайте.
  Технически: правки ADR 0001, README и комментариев в `gateway` и `notifier`. (`825e2da`)
- Событие о созданном заказе несёт позиции заказа. Технически: полезная нагрузка `booking.created` —
  `platform.BookingCreated` (`id`, `session_id`, `owner_id`, `total_rub`, `items`). (`29776b5`)

### Устарело

- Переменная `OPENROUTER_API_KEY`: используйте `LLM_API_KEY`. Технически: читается как запасное имя ключа,
  если `LLM_API_KEY` пуст, до конца фазы 1. (`29776b5`)

### Удалено

- API без версии `/api/...`, клиенты переходят на `/api/v1`. Технически: старые сессии без записи в
  `gateway.sessions` отвечают `404`, фронт в этом случае создаёт новую сессию. (`29776b5`, `5e6b0c4`)
- Контракт `inventory.v1`: его RPC разошлись по `catalog.v1` и `lodging.v1`. Технически: удалены
  `proto/inventory/v1` и `gen/inventory/v1`. (`29776b5`)
- Ручные TS-типы ответов API. Технически: удалён `web/src/api.ts`, типы — из `web/src/api/schema.ts`. (`5e6b0c4`)
- Поля отеля у заказов: заказ теперь состоит из позиций. Технически: миграция удаляет у `booking.orders`
  столбцы `hotel_id`, `hotel_name`, `check_in`, `nights`, `adults`, `children`, добавляет `owner_id` и
  `items`, переименовывает `total_price_rub` в `total_rub`; старые заказы без владельца не видны в `/orders`. (`29776b5`)

### Исправлено

- Ответы агента на соседние ходы склеивались в одно сообщение после правки карточки. Технически: флаг
  потоковой вставки в `web/src/App.tsx` вычисляется вне функции обновления состояния, которую StrictMode
  вызывает дважды. (`5e6b0c4`)

[unreleased]: https://github.com/lowcoware/ostrovok-hack-horkilion/commits/main
