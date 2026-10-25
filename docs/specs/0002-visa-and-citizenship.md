# Спека 0002. Визы и гражданство

- Статус: inbox
- Дата: 2026-10-25
- Основание: [ADR 0003](../adr/0003-visa-by-citizenship.md); меняет контракт [спеки 0001](0001-phase0-contracts.md)
- Ревью: Ask. Меняет `api/openapi.yaml` и `proto/`, поэтому ревью синхронное, бэкенд и фронт вместе, до мержа.
- Место в плане: первая задача фазы 1, `P1-BE-01` и `P1-FE-01` в [спеке 0003](0003-phase1-plan.md)

## Context

ADR 0003 расширяет географию: подбор по всем странам, у визовых стран пометка, виза считается по гражданству. Контракт фазы 0 этого не умеет: в `TripPlan.constraints` нет гражданства, у `Destination` нет визы, а `catalog.v1.SearchDestinationsRequest.russia_and_visa_free_only` жёстко задаёт старое правило. Эта спека меняет контракт так, чтобы бэкенд, данные и фронт могли работать параллельно.

## Non-Goals

- Транзитные визы и правила пересадок в третьих странах.
- Стоимость, сроки оформления и список документов для визы.
- Оформление визы, напоминание подать документы.
- Двойное гражданство: в плане одно гражданство.
- Въездные правила, не связанные с визой: прививки, страховка, срок действия паспорта.

## Assumptions

- Контракт фазы 0 смержен (PR #1), генерация кода — `scripts/gen-proto.sh` и `npm run gen:api`.
- Визовые данные приходят снапшотом от роли «Данные» в формате из раздела «Файлы снапшота» ниже и общего формата из спеки 0003 (CFC-3).

## Design Principles

- Контракт — источник правды (спека 0001): ручных копий типов нет, правка идёт PR, который меняет обе стороны.
- Справочные данные реальные, у каждой визовой записи есть `sources` (ADR 0002).
- Агент не вычисляет визу сам и не спрашивает её у LLM: только `catalog.GetVisaRequirement` (ADR 0003, альтернативы).

## Data Model / Contract

### Статусы визы

Один набор значений для API, proto и снапшота:

| API (`VisaStatus`) | proto (`VisaRequirement.Status`) | passport-index-data | Без визы для подбора |
|---|---|---|---|
| `home` | `STATUS_HOME` | `-1` (паспорт = страна) | да |
| `visa_free` | `STATUS_VISA_FREE` | `visa free` или число дней | да |
| `visa_on_arrival` | `STATUS_VISA_ON_ARRIVAL` | `visa on arrival` | да |
| `eta` | `STATUS_ETA` | `eta` | нет, оформляется онлайн |
| `e_visa` | `STATUS_E_VISA` | `e-visa` | нет, оформляется онлайн |
| `visa_required` | `STATUS_VISA_REQUIRED` | `visa required` | нет |
| `no_admission` | `STATUS_NO_ADMISSION` | `no admission` | нет, направление не предлагается |

### OpenAPI (`api/openapi.yaml`)

Новые схемы:

```yaml
VisaStatus:
  type: string
  enum: [home, visa_free, visa_on_arrival, eta, e_visa, visa_required, no_admission]
VisaRequirement:
  type: object
  required: [citizenship, country_code, status, sources]
  properties:
    citizenship: { type: string, pattern: "^[A-Z]{2}$" }   # паспорт, ISO 3166-1 alpha-2
    country_code: { type: string, pattern: "^[A-Z]{2}$" }  # страна назначения
    status: { $ref: "#/components/schemas/VisaStatus" }
    max_stay_days: { type: integer, minimum: 1 }           # безвизовый срок, если известен
    sources: { type: array, items: { $ref: "#/components/schemas/Source" } }
Country:
  type: object
  required: [code, name]
  properties:
    code: { type: string, pattern: "^[A-Z]{2}$" }
    name: { type: string }                                 # по-русски
```

Изменения существующих схем:

| Схема | Изменение |
|---|---|
| `Constraints` | `citizenship` (`^[A-Z]{2}$`, необязательное; пока не названо, агент ставит `RU` допущением) |
| `Preferences` | `visa_free_only` (boolean, в плане всегда есть, по умолчанию `true`) |
| `PreferencesPatch`, `PlanPatch` | `citizenship` в `PlanPatch`, `visa_free_only` в `PreferencesPatch` |
| `Destination` | `visa` (`VisaRequirement`, обязательное) |
| `TripPlan` | `visa` (`VisaRequirement`, необязательное) — виза в страну выбранного направления |
| `Check.kind` | новые значения `visa`, `stay_within_visa_free_days` |

Новые эндпоинты, без `security` (справочники):

| Метод и путь | Ответ |
|---|---|
| `GET /countries` | `{ "countries": Country[] }` — для выбора гражданства, отсортированы по имени, первой идёт `RU` |
| `GET /places?query=<строка>&kind=city` | `{ "places": Place[] }` — подсказки города отправления, до 10 штук; `query` от 2 до 100 символов, иначе `400` |

### proto

`proto/catalog/v1/catalog.proto`:

```proto
service CatalogService {
  rpc SearchDestinations(SearchDestinationsRequest) returns (SearchDestinationsResponse);
  rpc GetWeather(GetWeatherRequest) returns (Weather);
  rpc GetVisaRequirement(GetVisaRequirementRequest) returns (VisaRequirement);   // новый
  rpc ListCountries(ListCountriesRequest) returns (ListCountriesResponse);       // новый
  rpc SearchPlaces(SearchPlacesRequest) returns (SearchPlacesResponse);          // новый
}

message SearchDestinationsRequest {
  reserved 9;
  reserved "russia_and_visa_free_only";
  // поля 1–8 и 10 без изменений
  string citizenship = 11;    // ISO-2, пусто = RU
  bool visa_free_only = 12;   // только home, visa_free, visa_on_arrival
}

message Destination {
  // поля 1–10 без изменений
  VisaRequirement visa = 11;
}

message VisaRequirement {
  enum Status {
    STATUS_UNSPECIFIED = 0;
    STATUS_HOME = 1;
    STATUS_VISA_FREE = 2;
    STATUS_VISA_ON_ARRIVAL = 3;
    STATUS_ETA = 4;
    STATUS_E_VISA = 5;
    STATUS_VISA_REQUIRED = 6;
    STATUS_NO_ADMISSION = 7;
  }
  string citizenship = 1;
  string country_code = 2;
  Status status = 3;
  int32 max_stay_days = 4;    // 0 = неизвестно
  repeated common.v1.Source sources = 5;
}

message GetVisaRequirementRequest { string citizenship = 1; string country_code = 2; }
message ListCountriesRequest {}
message ListCountriesResponse { repeated Country countries = 1; }
message Country { string code = 1; string name_ru = 2; string name_en = 3; }
message SearchPlacesRequest {
  string query = 1;
  repeated common.v1.Place.Kind kinds = 2;   // пусто = города
  int32 limit = 3;                           // до 20
}
message SearchPlacesResponse { repeated common.v1.Place places = 1; }
```

Ошибки: неизвестный код страны или гражданства — `INVALID_ARGUMENT`; пары нет в данных — `NOT_FOUND`.

### Файлы снапшота

Формат каталога `data/`, манифест и общие правила — спека 0003, CFC-3. Для этой спеки два набора:

`data/snapshot/countries.jsonl`, одна страна на строку:

```json
{"code": "BY", "name_ru": "Беларусь", "name_en": "Belarus"}
```

`data/snapshot/visa.jsonl`, одна пара «паспорт — страна» на строку, все 199 паспортов:

```json
{"passport": "RU", "destination": "TR", "status": "visa_free", "days": 60, "sources": ["passport-index-data"]}
{"passport": "RU", "destination": "EG", "status": "visa_on_arrival", "days": 30, "sources": ["passport-index-data", "mid-ru"]}
```

`status` — значения столбца «API» из таблицы статусов. `days` — необязательное. `sources` — имена провайдеров из манифеста. Ручные правки по справке МИД лежат в `data/overrides/visa.csv` (`passport,destination,status,days,comment`) и вливаются в `visa.jsonl` скриптом сборки снапшота; бэкенд читает только `visa.jsonl`.

## Runtime Behavior

1. **Гражданство по умолчанию.** Когда в плане нет `citizenship`, `FillDefaults` ставит `RU` и добавляет допущение `{field: "citizenship", value: "RU", reason: "гражданство не названо"}`.
2. **Роутер.** Фразы «у меня паспорт Беларуси», «я гражданин Казахстана» дают патч `citizenship`. Город отправления — `origin`, как в фазе 0.
3. **Инвалидация** (дополнение к таблице `internal/agent/plan/patch.go`):

   | Изменение | Что устаревает |
   |---|---|
   | `citizenship` | `destination_options`, `visa`, проверки `visa` и `stay_within_visa_free_days`; отели и транспорт не трогаются |
   | `preferences.visa_free_only` | `destination_options` |
   | `destination` | дополнительно `visa` |
   | `dates.nights` | проверка `stay_within_visa_free_days` |

4. **Вдохновение.** `SearchDestinations` получает `citizenship` и `visa_free_only`. Направления со статусом `no_admission` не возвращаются никогда.
5. **Выбранное направление.** Когда направление выбрано, агент вызывает `GetVisaRequirement(citizenship, страна направления)` и кладёт ответ в `TripPlan.visa`.
6. **Проверки:**

   | `kind` | `ok` | `warn` | `fail` |
   |---|---|---|---|
   | `visa` | `home`, `visa_free`, `visa_on_arrival` | `eta`, `e_visa` | `visa_required`, `no_admission` |
   | `stay_within_visa_free_days` | ночей не больше `max_stay_days` или срок неизвестен | — | ночей больше `max_stay_days` |

   Проверки `fail` не блокируют оформление, но попадают в ответ агента.
7. **Пользователь назвал визовую страну сам** («хочу в Париж»): направление принимается, `visa_free_only` не мешает, в плане `visa` со статусом `visa_required` и проверка `fail`.
8. **Отказы.** Если `catalog` недоступен, ход не падает: `TripPlan.visa` остаётся пустым, проверка `visa` получает `warn` с текстом «не удалось проверить визовый режим». Если пары нет в данных — то же самое.

## Testing Strategy

| Что | Как |
|---|---|
| Схемы `VisaRequirement`, `Country`, новые поля | тесты сверки со схемой (`internal/apitest`), как в фазе 0 |
| Инвалидация при смене гражданства | юнит-тест `internal/agent/plan`: меняется `citizenship`, отели остаются, `destination_options` и `visa` сброшены |
| Проверки `visa` и `stay_within_visa_free_days` | табличный тест по всем статусам |
| `GetVisaRequirement`, `ListCountries`, `SearchPlaces` | юнит-тесты провайдера на маленьком тестовом снапшоте в `testdata/` |
| Формат `visa.jsonl` | валидатор снапшота роли «Данные» (спека 0003, `P1-DS-01`) |
| Фронт | фикстура моков содержит визовое и безвизовое направление, `npm test` сверяет её со схемой |

## Decision Log

- **Статусы — свои значения, а не строки passport-index-data.** Строки набора содержат пробелы и числа в одном столбце; свои `snake_case`-значения стабильны при смене источника.
- **`visa_free_only` по умолчанию `true`.** Подбор для большинства пользователей остаётся безвизовым, как в ТЗ; визовые страны появляются, когда пользователь снимает фильтр или называет страну сам.
- **`visa_on_arrival` считается «без визы» в подборе.** Для путешественника это не требует действий до поездки; карточка всё равно показывает точный статус.
- **Справочники `/countries` и `/places` без владельца.** Это публичные данные, а не данные пользователя.

## Acceptance Criteria

1. Когда пользователь не назвал гражданство, в плане `constraints.citizenship = "RU"` и есть допущение по полю `citizenship`.
2. Когда пользователь пишет «у меня паспорт Беларуси», в плане `constraints.citizenship = "BY"`.
3. Когда меняется `citizenship`, варианты проживания остаются, а `destination_options` и `visa` сбрасываются.
4. Пока `preferences.visa_free_only` равно `true`, в `destination_options` только направления со статусом визы `home`, `visa_free` или `visa_on_arrival`.
5. Направление со статусом `no_admission` не попадает в `destination_options` ни при каком фильтре.
6. Когда направление выбрано, в плане есть `visa` для пары «гражданство — страна направления».
7. Если для выбранного направления статус `visa_required`, в плане есть проверка `visa` со статусом `fail`.
8. Если ночей в поездке больше `visa.max_stay_days`, в плане есть проверка `stay_within_visa_free_days` со статусом `fail`.
9. Если `catalog` недоступен, ход завершается, а в плане есть проверка `visa` со статусом `warn`.
10. `GET /api/v1/countries` возвращает все страны снапшота, первой — `RU`.
11. Если `GET /api/v1/places` вызван с `query` короче 2 символов, gateway отвечает `400`.
12. У каждого `VisaRequirement` в ответах API в `sources` есть хотя бы один источник.

## Открытые вопросы

- **Q-005.** `no_admission` для выбранного пользователем направления: только проверка `fail`, как сейчас в спеке, или запрет оформления заказа? Предложение — только `fail`, данные справочные. Решает команда.
