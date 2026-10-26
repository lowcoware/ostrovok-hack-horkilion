# Деплой на сервер

Гайд по настройке автоматического деплоя через GitHub Actions: что подготовить на сервере, какие секреты и переменные завести в GitHub, как пройти первый деплой и как жить с ним дальше. Читается сверху вниз; в конце есть чеклист.

Все команды с `$` выполняются на вашей машине, с `server$` — на сервере под пользователем деплоя.

## 1. Как устроен деплой

Каждый push в `main` запускает workflow `Deploy` (`.github/workflows/deploy.yml`):

1. **CI** (`ci.yml`) определяет, что поменялось, и запускает только нужные проверки: Go (`gofmt`, `vet`, тесты, актуальность `gen/`), фронт (линт OpenAPI, актуальность TS-типов, тесты, сборка), сайт документации (сборка), compose (валидация).
2. **Сборка образов.** Изменившиеся группы собираются и публикуются в GitHub Container Registry (GHCR) с тегами `<sha коммита>` и `latest`:

   | Группа | Образы | Пересобирается, когда меняются |
   |---|---|---|
   | `backend` | `horkilion-gateway`, `-agent`, `-inventory`, `-booking`, `-notifier`, `-migrate` | `api/`, `cmd/`, `config/`, `internal/`, `gen/`, `migrations/`, `proto/`, `scripts/`, `go.mod`, `go.sum`, `Dockerfile` |
   | `web` | `horkilion-web` (фронт и Caddy) | `api/`, `web/`, `redocly.yaml` |
   | `docs` | `horkilion-docs` (сайт документации) | `docs/`, `docs-site/` |

   Изменение в `.github/` пересобирает всё.
3. **Деплой** по SSH:
   - на сервер копируются `docker-compose.yml`, `.env.example`, `server-env.sh` и конфиги из `deploy/`;
   - `server-env.sh` готовит `.env`: на первом деплое создаёт его со свежими секретами, на каждом записывает в него значения, которые хранятся в GitHub (ключ LLM, реестр образов, публичные адреса);
   - сервер логинится в GHCR токеном самого запуска;
   - `docker compose pull` и `up -d`: новый тег получает только изменившаяся группа, остальные контейнеры не пересоздаются. Текущие теги сервер хранит в файле `.image-tags`;
   - деплой ждёт до 2,5 минут, пока `gateway` не пройдёт проверку здоровья; если не прошёл, workflow падает и печатает логи сервисов;
   - старые образы удаляются, сервер разлогинивается из GHCR.

Одновременно идёт не больше одного деплоя: следующий ждёт окончания текущего.

На сервере работает полный профиль: приложение, PostgreSQL, NATS, Langfuse (с ClickHouse, Redis, MinIO), Prometheus, Grafana и сайт документации.

## 2. Требования к серверу

| Что | Минимум | Комфортно |
|---|---|---|
| ОС | Linux x86-64 с systemd: Ubuntu 24.04 LTS или Debian 12 | то же |
| Память | 4 ГБ (в простое стек занимает около 2,3 ГБ, 85% из них — Langfuse с ClickHouse) | 8 ГБ |
| Диск | 20 ГБ (образы около 7 ГБ плюс данные и логи) | 40 ГБ |
| CPU | 2 ядра | 4 ядра |
| ПО | Docker Engine с плагином compose v2, `openssl` | — |
| Сеть | Публичный IP; открыты 22 (SSH), 80 и 443; без доменов ещё 8080 (API), 3000 (Langfuse) и 3001 (Grafana) | Домен с A-записями на сервер |

Сборки на сервере нет: образы собирает CI.

## 3. Подготовка сервера

Делается один раз.

### 3.1. Docker

```bash
server$ curl -fsSL https://get.docker.com -o get-docker.sh
server$ sudo sh get-docker.sh
server$ docker --version && docker compose version
```

Скрипт ставит Docker Engine и плагин compose из официального репозитория Docker. Если политика запрещает `curl | sh`, поставьте пакеты по [инструкции Docker](https://docs.docker.com/engine/install/) для вашей ОС.

Проверьте, что есть `openssl` (на Ubuntu и Debian обычно стоит): `openssl version`, иначе `sudo apt-get install -y openssl`.

### 3.2. Пользователь для деплоя

Отдельный пользователь без пароля, вход только по ключу, доступ к Docker через группу:

```bash
server$ sudo adduser --disabled-password --gecos "" deploy
server$ sudo usermod -aG docker deploy
```

Группа `docker` даёт права, равные root. Поэтому ключ этого пользователя хранится только в секретах GitHub и у ответственного за сервер.

### 3.3. Ключ SSH для деплоя

Отдельная пара ключей только для GitHub Actions, без пароля, на вашей машине:

```bash
$ ssh-keygen -t ed25519 -C "horkilion-deploy" -N "" -f ./horkilion_deploy
```

Получится `horkilion_deploy` (приватный, уходит в секрет GitHub) и `horkilion_deploy.pub` (публичный, уходит на сервер). Положите публичный ключ пользователю `deploy`:

```bash
$ cat horkilion_deploy.pub        # скопировать строку
server$ sudo -u deploy mkdir -p -m 700 /home/deploy/.ssh
server$ echo '<строка из horkilion_deploy.pub>' | sudo -u deploy tee -a /home/deploy/.ssh/authorized_keys
server$ sudo chmod 600 /home/deploy/.ssh/authorized_keys
```

Проверьте вход и доступ к Docker:

```bash
$ ssh -i ./horkilion_deploy deploy@<адрес сервера> 'docker ps'
```

Команда должна вывести пустую таблицу контейнеров без ошибок. Ошибка `permission denied ... docker.sock` значит, что пользователь не в группе `docker` или сессия открыта до добавления в группу.

После того как приватный ключ окажется в секретах GitHub (раздел 4), удалите оба файла с машины или уберите их в менеджер паролей.

### 3.4. Отпечаток сервера (known_hosts)

Workflow подключается со строгой проверкой ключа сервера, поэтому отпечаток нужно сохранить заранее:

```bash
$ ssh-keyscan -p 22 <адрес сервера> 2>/dev/null > known_hosts.txt
$ ssh-keygen -lf known_hosts.txt
```

Сверьте отпечатки с тем, что показывает сам сервер, — так вы убедитесь, что `ssh-keyscan` говорил именно с ним:

```bash
server$ for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done
```

Содержимое `known_hosts.txt` целиком уходит в секрет `SSH_KNOWN_HOSTS`. Если SSH на нестандартном порту, укажите его в `-p`.

### 3.5. Файрвол

Если включён `ufw`:

```bash
server$ sudo ufw allow OpenSSH
server$ sudo ufw allow 80,443/tcp
server$ sudo ufw allow 443/udp      # HTTP/3
server$ sudo ufw allow 8080,3000,3001/tcp   # только без доменов: API, Langfuse и Grafana по портам
server$ sudo ufw enable
```

Docker публикует порты в обход `ufw`: закрыть порт контейнера правилом `ufw` нельзя. Наружу у нас публикует порты только контейнер `web` (Caddy): 80, 443, 8080, 3000 и 3001. Закрыть 8080, 3000 и 3001 можно файрволом провайдера (security group) или переводом API, Langfuse и Grafana на домены (раздел 3.6): тогда Caddy обслуживает их на 443, а на этих портах никто не слушает.

### 3.6. Домены (необязательно)

Без доменов приложение работает по `http://<IP>`. Для HTTPS заведите A-записи на IP сервера, например:

| Запись | Для чего |
|---|---|
| `app.example.com` | приложение и документация (`/docs/`) |
| `api.example.com` | API агента для сторонних клиентов |
| `langfuse.example.com` | Langfuse |
| `grafana.example.com` | Grafana |

Наш фронт ходит в API по пути `/api` своего же адреса, поэтому ему не нужны CORS и межсайтовые cookie. Отдельный `api.example.com` — точка входа для сторонних клиентов агента: он проксирует всё на `gateway`. Запросы из чужого браузерного фронта на него потребуют CORS, сейчас gateway его не отдаёт.

Без AAAA-записей на этих именах: иначе Let's Encrypt может проверять домен по IPv6 мимо сервера. Если DNS в Cloudflare, ставьте режим «DNS only» (серое облако), иначе Caddy не получит сертификат сам. TTL лучше 300 секунд, чтобы быстро исправить ошибку.

Caddy сам получит сертификаты Let's Encrypt при первом запросе, если записи уже смотрят на сервер и порты 80 и 443 открыты. Адреса задаются переменными в GitHub (раздел 4.3).

## 4. Настройка GitHub

### 4.1. Окружение `production`

Repository → Settings → Environments → New environment → `production`.

Секреты кладём в окружение, а не на уровень репозитория: так их видит только job деплоя. По желанию в окружении можно включить Required reviewers, тогда каждый деплой будет ждать подтверждения.

### 4.2. Секреты окружения

Settings → Environments → `production` → Environment secrets → Add secret.

| Секрет | Обязателен | Значение | Где взять |
|---|---|---|---|
| `SSH_HOST` | да | IP или домен сервера | — |
| `SSH_USER` | да | `deploy` | раздел 3.2 |
| `SSH_PORT` | нет | порт SSH, по умолчанию 22 | — |
| `SSH_PRIVATE_KEY` | да | содержимое файла `horkilion_deploy` целиком, включая строки `-----BEGIN` и `-----END` | раздел 3.3 |
| `SSH_KNOWN_HOSTS` | да | содержимое `known_hosts.txt` | раздел 3.4 |
| `LLM_API_KEY` | нет | ключ OpenRouter (или другого OpenAI-совместимого провайдера) | у ответственного за ключи; локально лежит в `deploy/.env` |

Без `LLM_API_KEY` агент работает на правилах и шаблонных ответах. Если секрет задан, каждый деплой записывает его в `.env` сервера; если секрет удалить, в `.env` останется последнее записанное значение.

### 4.3. Переменные окружения

Settings → Environments → `production` → Environment variables. Все необязательные: пустая переменная оставляет значение в `.env` сервера как есть.

| Переменная | По умолчанию | Пример с доменами | Что делает |
|---|---|---|---|
| `DEPLOY_PATH` | `horkilion` | `horkilion` | Каталог на сервере относительно домашнего каталога пользователя |
| `SITE_ADDRESS` | `:80` | `app.example.com` | Адрес приложения и документации |
| `API_ADDRESS` | `:8080` | `api.example.com` | Адрес API агента для сторонних клиентов |
| `LANGFUSE_ADDRESS` | `:3000` | `langfuse.example.com` | Адрес Langfuse |
| `GRAFANA_ADDRESS` | `:3001` | `grafana.example.com` | Адрес Grafana |
| `LANGFUSE_URL` | `http://localhost:3000` | `https://langfuse.example.com` | Публичный URL Langfuse для ссылок входа. Без доменов укажите `http://<IP>:3000` |

### 4.4. Права workflow

Settings → Actions → General:

- Actions permissions: разрешены actions, которые использует workflow (`actions/*`, `docker/*`); по умолчанию разрешены все.
- Workflow permissions можно оставить «Read repository contents»: нужные права (`packages: write`) workflow запрашивает сам.

### 4.5. Через gh CLI вместо веб-интерфейса

То же самое из терминала. Нужен `gh`, авторизованный аккаунтом с правами администратора репозитория:

```bash
$ gh auth login
$ R=<owner>/<repo>
$ gh api -X PUT repos/$R/environments/production

$ gh secret set SSH_HOST        --env production --repo $R --body '<адрес сервера>'
$ gh secret set SSH_USER        --env production --repo $R --body 'deploy'
$ gh secret set SSH_PRIVATE_KEY --env production --repo $R < horkilion_deploy
$ gh secret set SSH_KNOWN_HOSTS --env production --repo $R < known_hosts.txt
$ grep '^LLM_API_KEY=' deploy/.env | cut -d= -f2- | tr -d '\n' | gh secret set LLM_API_KEY --env production --repo $R

$ gh variable set SITE_ADDRESS --env production --repo $R --body 'app.example.com'
$ gh secret list --env production --repo $R
```

Значения секретов передаются из файлов и через stdin, поэтому не остаются в истории shell. `gh secret list` показывает только имена и даты, значения прочитать обратно нельзя.

## 5. Первый деплой

1. Убедитесь, что всё из разделов 3 и 4 сделано.
2. Запустите деплой: push в `main` или Actions → Deploy → Run workflow (ветка `main`). Ручной запуск пересобирает все образы.
3. Следите за job `deploy`. На первом запуске шаг «Prepare .env on the server» печатает `.env created with fresh secrets`, а шаг «Pull and start» — `gateway is healthy`.
4. Первый деплой занимает дольше обычного: сервер скачивает около 7 ГБ образов, Langfuse поднимает свои базы.

### 5.1. Видимость образов для жюри

Пакеты в GHCR после первой публикации обычно приватные. Серверу это не мешает: он логинится токеном запуска. Но `./run.sh` у жюри скачивает образы без логина, поэтому пакеты нужно сделать публичными:

Профиль владельца → Packages → `horkilion-<сервис>` → Package settings → Danger Zone → Change visibility → Public.

Это нужно сделать для каждого из восьми пакетов: `gateway`, `agent`, `inventory`, `booking`, `notifier`, `migrate`, `web`, `docs`. Проверка с любой машины без логина:

```bash
$ docker logout ghcr.io; docker pull ghcr.io/<owner>/horkilion-gateway:latest
```

## 6. Проверка после деплоя

| Что | Адрес без доменов | Вход |
|---|---|---|
| Приложение | `http://<IP>/` | — |
| Документация | `http://<IP>/docs/` | — |
| API для сторонних клиентов | `http://<IP>:8080/api/v1/...` | — |
| Langfuse | `http://<IP>:3000` | `LANGFUSE_INIT_USER_EMAIL` (по умолчанию `admin@horkilion.local`) и `LANGFUSE_INIT_USER_PASSWORD` из `.env` сервера |
| Grafana | `http://<IP>:3001` | `admin` и `GRAFANA_ADMIN_PASSWORD` из `.env` сервера |

Пароли посмотреть на сервере:

```bash
server$ grep -E '^(LANGFUSE_INIT_USER_EMAIL|LANGFUSE_INIT_USER_PASSWORD|GRAFANA_ADMIN_PASSWORD)=' ~/horkilion/.env
```

Сквозная проверка: в приложении отправить запрос вроде «хочу к тёплому морю в ноябре, недорого, с ребёнком», дойти до заказа, затем найти трейс хода в Langfuse.

## 6.1. Мониторинг

В Grafana в папке Horkilion четыре дашборда, домашняя страница — «сервисы»:

| Дашборд | Что показывает | Откуда данные |
|---|---|---|
| Horkilion: сервисы | Живы ли сервисы; HTTP gateway: запросы, 4xx и 5xx, задержка p95 по маршрутам; gRPC между сервисами: вызовы, ошибки, задержка; память, CPU и горутины каждого сервиса | Prometheus, метрики сервисов |
| Horkilion: агент и LLM | Ходы агента и их длительность; вызовы LLM по типу (router, tools, reply), ошибки, задержка, токены по моделям | Prometheus, метрики агента |
| Horkilion: продуктовая воронка | Сессии, заказы, выручка по демо-ценам, средний чек, доля поездок целиком, воронка до оплаты, последние заказы | ClickHouse, база `analytics` |
| Horkilion: сервер и контейнеры | CPU, память, диск, сеть сервера; сколько CPU, памяти и сети ест каждый контейнер | node-exporter и cAdvisor |

Дашборды хранятся кодом: JSON в `deploy/grafana/dashboards` генерирует `deploy/grafana/gen_dashboards.py`. Правки в интерфейсе Grafana не сохраняются; чтобы изменить дашборд, поправьте генератор, выполните `python3 deploy/grafana/gen_dashboards.py deploy/grafana/dashboards` и закоммитьте результат. Промпты и ответы моделей смотрят не в Grafana, а в Langfuse.

## 7. Обычная работа

- **Выкатить изменение:** смержить PR в `main`. Правка только во фронте перезапускает один контейнер `web`, правка документации — один `docs`, бэкенд при этом не трогается.
- **Перезапустить деплой без изменений:** Actions → Deploy → Run workflow.
- **Поменять ключ LLM:** обновить секрет `LLM_API_KEY` и перезапустить деплой.
- **Поменять адреса:** обновить переменные из раздела 4.3 и перезапустить деплой.

### 7.1. Ручные команды на сервере

Compose берёт теги образов из окружения, а актуальные теги лежат в `.image-tags`. Поэтому перед ручными командами их нужно загрузить, иначе compose возьмёт `latest` из `.env`:

```bash
server$ cd ~/horkilion
server$ set -a; . ./.image-tags; set +a; export IMAGE_REGISTRY=$(sed -n 's/^IMAGE_REGISTRY=//p' .env)

server$ docker compose ps                         # состояние сервисов
server$ docker compose logs -f --tail=100 agent   # логи одного сервиса
server$ docker compose restart gateway            # перезапуск
server$ docker compose up -d                      # применить правку .env
```

`pull` вручную требует логина в GHCR (`docker login ghcr.io` с PAT со scope `read:packages`), если пакеты приватные.

### 7.2. Что можно править в `.env` сервера

Всё, кроме значений, которые workflow перезаписывает на каждом деплое: `IMAGE_REGISTRY`, `LLM_API_KEY` (если задан секрет) и адреса, если заданы переменные из раздела 4.3. Модели (`LLM_MODEL`, `LLM_ROUTER_MODEL`), поставщики (`*_PROVIDER`), профиль (`COMPOSE_PROFILES`) правятся прямо в `.env`, после чего `docker compose up -d` по командам из 7.1.

Сгенерированные секреты (`POSTGRES_PASSWORD` и другие) после первого запуска не меняйте: базы уже инициализированы с ними, и сервисы перестанут к ним подключаться.

## 8. Откат

- **Обычный путь:** `git revert` проблемного коммита и push в `main`. Деплой выкатит предыдущее состояние кода.
- **Быстро, на сервере:** вернуть в `.image-tags` тег предыдущего удачного коммита для нужной группы (SHA видны в истории запусков Deploy) и применить:

  ```bash
  server$ cd ~/horkilion && nano .image-tags   # например, WEB_TAG=<sha предыдущего коммита>
  server$ set -a; . ./.image-tags; set +a; export IMAGE_REGISTRY=$(sed -n 's/^IMAGE_REGISTRY=//p' .env)
  server$ docker compose up -d
  ```

  Следующий деплой из `main` перезапишет теги, поэтому за быстрым откатом всё равно должен идти revert.

Миграции базы только накатываются вперёд. Откат кода после миграции, которая меняет схему несовместимо, требует отдельного решения.

## 9. Бэкапы

Данные живут в томах Docker с префиксом каталога деплоя (`horkilion_postgres_data` и другие). Ценное — PostgreSQL: планы, заказы, аккаунты, а также база Langfuse.

```bash
server$ cd ~/horkilion && mkdir -p backups
server$ docker compose exec -T postgres pg_dumpall -U horkilion | gzip > backups/pg-$(date +%F-%H%M).sql.gz
```

Восстановление на пустой базе:

```bash
server$ gunzip -c backups/pg-<дата>.sql.gz | docker compose exec -T postgres psql -U horkilion -d postgres
```

Храните копии не только на том же сервере. `docker compose down -v` удаляет тома вместе с данными; без `-v` данные сохраняются.

## 10. Решение проблем

| Симптом | Причина и что делать |
|---|---|
| `Permission denied (publickey)` в шаге SSH | В `SSH_PRIVATE_KEY` не тот ключ или он вставлен не целиком; публичный ключ не в `authorized_keys` нужного пользователя; права на `~/.ssh` шире 700 и 600 |
| `Host key verification failed` | `SSH_KNOWN_HOSTS` пустой, устарел (сервер переустановлен) или снят с другого порта. Повторить раздел 3.4 |
| `permission denied while trying to connect to the Docker daemon socket` | Пользователь деплоя не в группе `docker` (раздел 3.2) |
| `openssl is required to generate secrets` | На сервере нет `openssl`, поставить пакет |
| `denied` или `unauthorized` при `docker compose pull` | Образ ещё не опубликован (первая сборка упала) или нет доступа к пакету. Проверить job `build` и вкладку Packages |
| `gateway is healthy` не появляется, workflow печатает логи | Смотреть логи `migrate`, `gateway`, `agent`, `booking` в выводе шага. Частое: миграция упала, неверный пароль базы после ручной правки `.env` |
| Сервисы перезапускаются, `docker compose ps` показывает `Restarting`, в `dmesg` есть `Out of memory` | Не хватает памяти для полного профиля. Добавить память или выключить полный профиль: `COMPOSE_PROFILES=` и `ANALYTICS_URL=` в `.env` |
| Caddy не выдаёт HTTPS, в логах `web` ошибки ACME | A-записи не смотрят на сервер или закрыт порт 80. У Let's Encrypt есть лимиты на число выпусков: не перезапускайте бесконечно с неверным DNS |
| `bind: address already in use` для порта 80 | На сервере уже работает nginx или apache. Остановить его или освободить порт |
| Деплой прошёл, а изменения не видны | Изменилась не та группа, которая пересобирается (таблица в разделе 1), или кэш браузера. Посмотреть строку `backend: …, web: …, docs: …` в шаге «Pull and start» |

## 11. Безопасность

- Приватный ключ деплоя существует только в секретах GitHub. При подозрении на утечку: удалить строку из `authorized_keys`, сгенерировать новую пару, обновить секрет.
- `.env` на сервере создаётся с правами 600; в git он не попадает никогда.
- Ключ LLM передаётся на сервер через stdin, не появляется в командной строке и в логах workflow.
- Токен GHCR — временный токен запуска, сервер разлогинивается в конце каждого деплоя.
- Langfuse и Grafana открыты в интернет со своими паролями. Если они не нужны снаружи, закройте порты 3000 и 3001 файрволом провайдера: `ufw` их не закроет (раздел 3.5).

## 12. Чеклист

Сервер:

- [ ] Docker Engine и compose v2 установлены, `openssl` есть
- [ ] Пользователь `deploy` в группе `docker`, вход по ключу работает, `docker ps` без ошибок
- [ ] Открыты 22, 80, 443 (и 3000, 3001, если нужны по портам)
- [ ] Отпечаток сервера сверен
- [ ] A-записи доменов смотрят на сервер (если нужен HTTPS)

GitHub:

- [ ] Окружение `production` создано
- [ ] Секреты `SSH_HOST`, `SSH_USER`, `SSH_PRIVATE_KEY`, `SSH_KNOWN_HOSTS` (и `SSH_PORT`, если не 22)
- [ ] Секрет `LLM_API_KEY`
- [ ] Переменные адресов и `LANGFUSE_URL`, если есть домены

После первого деплоя:

- [ ] Job `deploy` зелёный, в логе `gateway is healthy`
- [ ] Открываются приложение, `/docs/`, Langfuse, Grafana
- [ ] Сквозной сценарий до заказа проходит, трейс есть в Langfuse
- [ ] Восемь пакетов в GHCR публичные, `docker pull` без логина работает
- [ ] Файлы ключа деплоя удалены с локальной машины
