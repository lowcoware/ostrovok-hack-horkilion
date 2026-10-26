"""Generates the provisioned Grafana dashboards for horkilion.

Edit the dashboards here, not in the Grafana UI, then regenerate:

    python3 deploy/grafana/gen_dashboards.py deploy/grafana/dashboards

Standard library only. Data source uids match deploy/grafana/provisioning/datasources.
"""
import json
import sys
from pathlib import Path

PROM = {"type": "prometheus", "uid": "prometheus"}
CH = {"type": "grafana-clickhouse-datasource", "uid": "clickhouse"}
RI = "[$__rate_interval]"


class Board:
    def __init__(self, uid, title, description, tags):
        self.uid, self.title, self.description, self.tags = uid, title, description, tags
        self.panels, self.y, self.x, self.row_h, self.next_id = [], 0, 0, 0, 1

    def row(self, title):
        self._newline()
        self.panels.append({"type": "row", "title": title, "collapsed": False, "id": self._id(),
                            "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []})
        self.y += 1

    def add(self, panel, w, h):
        if self.x + w > 24:
            self._newline()
        panel["id"] = self._id()
        panel["gridPos"] = {"h": h, "w": w, "x": self.x, "y": self.y}
        self.panels.append(panel)
        self.x += w
        self.row_h = max(self.row_h, h)

    def _newline(self):
        self.y += self.row_h
        self.x, self.row_h = 0, 0

    def _id(self):
        self.next_id += 1
        return self.next_id - 1

    def json(self, refresh="30s", time_from="now-6h"):
        return {
            "uid": self.uid, "title": self.title, "description": self.description, "tags": self.tags,
            "schemaVersion": 41, "version": 1, "editable": True, "graphTooltip": 1,
            "refresh": refresh, "time": {"from": time_from, "to": "now"},
            "timepicker": {}, "timezone": "browser", "templating": {"list": []},
            "annotations": {"list": []}, "links": [
                {"title": "Дашборды проекта", "type": "dashboards", "tags": ["horkilion"], "asDropdown": True,
                 "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "external link"}],
            "panels": self.panels,
        }


def prom(expr, legend="__auto", instant=False):
    t = {"datasource": PROM, "expr": expr, "legendFormat": legend, "refId": "A"}
    if instant:
        t["instant"], t["range"] = True, False
    return t


def sql(raw, fmt="table"):
    return {"datasource": CH, "editorType": "sql", "queryType": "timeseries" if fmt == "timeseries" else "table",
            "format": 0 if fmt == "timeseries" else 1, "rawSql": raw, "refId": "A"}


def refs(targets):
    for i, t in enumerate(targets):
        t["refId"] = chr(ord("A") + i)
    return targets


def timeseries(title, targets, unit="short", desc="", stack=False, fill=10, ds=PROM, decimals=None):
    defaults = {"unit": unit, "custom": {"lineWidth": 2, "fillOpacity": fill, "showPoints": "never",
                                         "spanNulls": True,
                                         "stacking": {"mode": "normal" if stack else "none", "group": "A"}}}
    if decimals is not None:
        defaults["decimals"] = decimals
    return {"type": "timeseries", "title": title, "description": desc, "datasource": ds,
            "targets": refs(targets), "fieldConfig": {"defaults": defaults, "overrides": []},
            "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                        "tooltip": {"mode": "multi", "sort": "desc"}}}


def stat(title, targets, unit="short", desc="", thresholds=None, ds=PROM, decimals=None, mappings=None,
         color_mode="value", text_mode="auto"):
    steps = thresholds or [{"color": "green", "value": None}]
    defaults = {"unit": unit, "thresholds": {"mode": "absolute", "steps": steps}, "mappings": mappings or []}
    if decimals is not None:
        defaults["decimals"] = decimals
    return {"type": "stat", "title": title, "description": desc, "datasource": ds, "targets": refs(targets),
            "fieldConfig": {"defaults": defaults, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "colorMode": color_mode, "graphMode": "none", "textMode": text_mode,
                        "justifyMode": "auto", "orientation": "auto", "wideLayout": True}}


def bargauge(title, targets, unit="short", desc="", ds=PROM, display="gradient", values=False):
    return {"type": "bargauge", "title": title, "description": desc, "datasource": ds, "targets": refs(targets),
            "fieldConfig": {"defaults": {"unit": unit, "min": 0, "color": {"mode": "continuous-BlYlRd"},
                                         "thresholds": {"mode": "absolute",
                                                        "steps": [{"color": "green", "value": None}]}},
                            "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": values},
                        "orientation": "horizontal", "displayMode": display, "showUnfilled": True,
                        "valueMode": "color", "namePlacement": "left", "sizing": "auto"}}


def table(title, targets, desc="", ds=CH, overrides=None):
    return {"type": "table", "title": title, "description": desc, "datasource": ds, "targets": refs(targets),
            "fieldConfig": {"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"}}},
                            "overrides": overrides or []},
            "options": {"showHeader": True, "cellHeight": "sm", "footer": {"show": False}}}


UP_MAP = [{"type": "value", "options": {"0": {"text": "лежит", "color": "red", "index": 0},
                                        "1": {"text": "работает", "color": "green", "index": 1}}}]
PCT_STEPS = [{"color": "green", "value": None}, {"color": "orange", "value": 70}, {"color": "red", "value": 90}]
ERR_STEPS = [{"color": "green", "value": None}, {"color": "orange", "value": 0.01}, {"color": "red", "value": 0.05}]


def services():
    b = Board("horkilion-services", "Horkilion: сервисы",
              "Здоровье Go-сервисов: HTTP gateway, gRPC между сервисами, рантайм Go.", ["horkilion"])
    b.row("Состояние")
    b.add(stat("Сервисы", [prom('up{job="services"}', "{{service}}")], mappings=UP_MAP,
               thresholds=[{"color": "red", "value": None}, {"color": "green", "value": 1}],
               color_mode="background", text_mode="value_and_name",
               desc="Отвечает ли /metrics каждого сервиса. Красный — Prometheus не может его опросить."), 24, 4)

    b.row("HTTP gateway")
    b.add(timeseries("Запросы по маршрутам", [prom(
        f'sum by (route, method) (rate(http_requests_total{{route!="unmatched"}}{RI}))', "{{method}} {{route}}")],
        unit="reqps", desc="Запросы в секунду к внешнему API."), 12, 8)
    b.add(timeseries("Доля ошибок 5xx", [prom(
        f'sum(rate(http_requests_total{{code=~"5.."}}{RI})) / clamp_min(sum(rate(http_requests_total{RI})), 1e-9)',
        "5xx")], unit="percentunit", desc="Доля ответов gateway с кодом 5xx."), 6, 8)
    b.add(timeseries("Ответы 4xx по кодам", [prom(
        f'sum by (code) (rate(http_requests_total{{code=~"4.."}}{RI}))', "{{code}}")], unit="reqps",
        desc="404 — в том числе чужие сессии, 429 — срабатывание rate limit."), 6, 8)
    b.add(timeseries("Задержка p95 по маршрутам", [prom(
        f'histogram_quantile(0.95, sum by (le, route) (rate(http_request_duration_seconds_bucket'
        f'{{route!~".*/events|unmatched"}}{RI})))', "{{route}}")], unit="s",
        desc="Без потока событий SSE: он длится, пока открыта вкладка."), 12, 8)
    b.add(timeseries("Подключения к потоку SSE", [prom(
        f'sum(rate(http_requests_total{{route=~".*/events"}}{RI}))', "новые подключения")], unit="reqps",
        desc="Как часто клиенты подключаются к потоку событий хода агента."), 12, 8)

    b.row("gRPC между сервисами")
    b.add(timeseries("Вызовы по методам", [prom(
        f'sum by (grpc_method) (rate(grpc_server_handled_total{RI}))', "{{grpc_method}}")], unit="reqps"), 8, 8)
    b.add(timeseries("Ошибки по кодам", [prom(
        f'sum by (grpc_method, grpc_code) (rate(grpc_server_handled_total{{grpc_code!="OK"}}{RI}))',
        "{{grpc_method}} {{grpc_code}}")], unit="reqps",
        desc="NotFound и InvalidArgument — обычно ошибка запроса, Unavailable и Internal — сбой сервиса."), 8, 8)
    b.add(timeseries("Задержка p95 по методам", [prom(
        f'histogram_quantile(0.95, sum by (le, grpc_method) (rate(grpc_server_handling_seconds_bucket{RI})))',
        "{{grpc_method}}")], unit="s"), 8, 8)

    b.row("Рантайм Go")
    b.add(timeseries("Память процесса (RSS)", [prom('process_resident_memory_bytes{job="services"}',
                                                    "{{service}}")], unit="bytes"), 8, 8)
    b.add(timeseries("CPU", [prom(f'rate(process_cpu_seconds_total{{job="services"}}{RI})', "{{service}}")],
                     unit="percentunit", desc="Доля одного ядра."), 8, 8)
    b.add(timeseries("Горутины", [prom('go_goroutines{job="services"}', "{{service}}")]), 8, 8)
    return b


def agent():
    b = Board("horkilion-agent", "Horkilion: агент и LLM",
              "Ходы агента и вызовы моделей. Промпты и ответы каждого вызова — в Langfuse.", ["horkilion"])
    b.row("Сводка за выбранный период")
    b.add(stat("Ходов агента", [prom('sum(increase(agent_turns_total[$__range]))', instant=True)],
               decimals=0), 4, 4)
    b.add(stat("Ходов с ошибкой", [prom(
        'sum(increase(agent_turns_total{result="error"}[$__range])) / clamp_min(sum(increase(agent_turns_total[$__range])), 1e-9)',
        instant=True)], unit="percentunit", thresholds=ERR_STEPS, decimals=1), 4, 4)
    b.add(stat("Ход, p95", [prom(
        'histogram_quantile(0.95, sum by (le) (increase(agent_turn_duration_seconds_bucket[$__range])))',
        instant=True)], unit="s", decimals=1,
        thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 20}, {"color": "red", "value": 60}]),
        4, 4)
    b.add(stat("Вызовов LLM", [prom('sum(increase(llm_requests_total[$__range]))', instant=True)], decimals=0), 4, 4)
    b.add(stat("Ошибки LLM", [prom(
        'sum(increase(llm_requests_total{result="error"}[$__range])) / clamp_min(sum(increase(llm_requests_total[$__range])), 1e-9)',
        instant=True)], unit="percentunit", thresholds=ERR_STEPS, decimals=1), 4, 4)
    b.add(stat("Токенов", [prom('sum by (type) (increase(llm_tokens_total[$__range]))', "{{type}}", instant=True)],
               decimals=0, text_mode="value_and_name"), 4, 4)

    b.row("Ходы агента")
    b.add(timeseries("Ходы в минуту", [prom(f'sum by (result) (rate(agent_turns_total{RI})) * 60', "{{result}}")],
                     stack=True, fill=30), 12, 8)
    b.add(timeseries("Длительность хода", refs([
        prom(f'histogram_quantile(0.5, sum by (le) (rate(agent_turn_duration_seconds_bucket{RI})))', "p50"),
        prom(f'histogram_quantile(0.95, sum by (le) (rate(agent_turn_duration_seconds_bucket{RI})))', "p95")]),
        unit="s", desc="От получения сообщения до сохранения плана, включая вызовы LLM и поставщиков."), 12, 8)

    b.row("LLM")
    b.add(timeseries("Вызовы по типу", [prom(
        f'sum by (call, result) (rate(llm_requests_total{RI})) * 60', "{{call}} {{result}}")],
        desc="router — разбор сообщения, tools — цикл с инструментами, reply — потоковый ответ. В минуту."), 8, 8)
    b.add(timeseries("Задержка p95 по типу", [prom(
        f'histogram_quantile(0.95, sum by (le, call) (rate(llm_request_duration_seconds_bucket{RI})))', "{{call}}")],
        unit="s"), 8, 8)
    b.add(timeseries("Токены в минуту", [prom(
        f'sum by (model, type) (rate(llm_tokens_total{RI})) * 60', "{{model}} {{type}}")], stack=True, fill=30),
        8, 8)
    return b


FUNNEL = """windowFunnel(604800)(
        toDateTime(event_time),
        event = 'session_started',
        event = 'stage_entered' AND stage IN ('compare', 'assemble'),
        event = 'option_selected',
        event = 'checkout_completed'
    )"""


def funnel():
    b = Board("horkilion-funnel", "Horkilion: продуктовая воронка",
              "События воронки из ClickHouse (база analytics): от первого сообщения до покупки.",
              ["horkilion"])
    b.row("Сводка за выбранный период")
    b.add(stat("Сессий начато", [sql(
        "SELECT count() FROM analytics.events WHERE event = 'session_started' AND $__timeFilter(event_time)")],
        ds=CH, decimals=0), 4, 4)
    b.add(stat("Ходов агента", [sql(
        "SELECT count() FROM analytics.events WHERE event = 'turn_completed' AND $__timeFilter(event_time)")],
        ds=CH, decimals=0), 4, 4)
    b.add(stat("Заказов", [sql(
        "SELECT count() FROM analytics.events WHERE event = 'checkout_completed' AND $__timeFilter(event_time)")],
        ds=CH, decimals=0), 4, 4)
    b.add(stat("Выручка (демо)", [sql(
        "SELECT sum(JSONExtractInt(props, 'total_rub')) FROM analytics.events "
        "WHERE event = 'checkout_completed' AND $__timeFilter(event_time)")],
        ds=CH, unit="currencyRUB", decimals=0, desc="Сумма заказов по синтетическим ценам."), 4, 4)
    b.add(stat("Средний чек", [sql(
        "SELECT avg(JSONExtractInt(props, 'total_rub')) FROM analytics.events "
        "WHERE event = 'checkout_completed' AND $__timeFilter(event_time)")],
        ds=CH, unit="currencyRUB", decimals=0), 4, 4)
    b.add(stat("Поездка целиком", [sql(
        "SELECT countIf(JSONExtractBool(props, 'full_trip')) / greatest(count(), 1) FROM analytics.events "
        "WHERE event = 'checkout_completed' AND $__timeFilter(event_time)")],
        ds=CH, unit="percentunit", decimals=0,
        desc="Доля заказов, где куплены отель, дорога туда и обратно и оба трансфера."), 4, 4)

    b.row("Воронка")
    b.add(bargauge("Сколько сессий дошли до шага", [sql(f"""SELECT
    countIf(level >= 1) AS `1. Начали разговор`,
    countIf(level >= 2) AS `2. Увидели отели`,
    countIf(level >= 3) AS `3. Выбрали вариант`,
    countIf(level >= 4) AS `4. Оплатили`
FROM
(
    SELECT session_id, {FUNNEL} AS level
    FROM analytics.events
    WHERE $__timeFilter(event_time)
    GROUP BY session_id
)""")], ds=CH, desc="windowFunnel за 7 дней: шаги должны идти по порядку в одной сессии."), 12, 9)
    b.add(stat("Конверсия в покупку", [sql(f"""SELECT countIf(level >= 4) / greatest(countIf(level >= 1), 1)
FROM
(
    SELECT session_id, {FUNNEL} AS level
    FROM analytics.events
    WHERE $__timeFilter(event_time)
    GROUP BY session_id
)""")], ds=CH, unit="percentunit", decimals=1, desc="Доля начатых сессий, которые дошли до оплаты."), 4, 9)
    b.add(bargauge("На каком этапе были ходы", [sql(
        "SELECT stage, count() AS turns FROM analytics.events "
        "WHERE event = 'turn_completed' AND $__timeFilter(event_time) GROUP BY stage ORDER BY turns DESC")],
        ds=CH, values=True, desc="Этап плана после хода агента."), 8, 9)

    b.row("Динамика")
    b.add(timeseries("События", [sql(
        "SELECT $__timeInterval(event_time) AS time, event, count() AS events FROM analytics.events "
        "WHERE $__timeFilter(event_time) GROUP BY time, event ORDER BY time", "timeseries")],
        ds=CH, stack=True, fill=30), 12, 8)
    b.add(timeseries("Средняя длительность хода", [
        sql("SELECT $__timeInterval(event_time) AS time, avg(JSONExtractInt(props, 'duration_ms')) / 1000 AS `средний ход, с` "
            "FROM analytics.events WHERE event = 'turn_completed' AND $__timeFilter(event_time) GROUP BY time ORDER BY time",
            "timeseries")], ds=CH, unit="s", desc="Из событий turn_completed."), 6, 8)
    b.add(timeseries("Пересборки плана", [sql(
        "SELECT $__timeInterval(event_time) AS time, count() AS `пересборок` FROM analytics.events "
        "WHERE event = 'plan_rebuilt' AND $__timeFilter(event_time) GROUP BY time ORDER BY time", "timeseries")],
        ds=CH, desc="Изменение условий, которое инвалидировало часть плана."), 6, 8)

    b.row("Последние заказы и выборы")
    b.add(table("Заказы", [sql(
        "SELECT event_time AS `время`, JSONExtractString(props, 'order_id') AS `заказ`, "
        "JSONExtractInt(props, 'total_rub') AS `сумма, ₽`, JSONExtractInt(props, 'items') AS `позиций`, "
        "if(JSONExtractBool(props, 'full_trip'), 'да', 'нет') AS `поездка целиком` "
        "FROM analytics.events WHERE event = 'checkout_completed' AND $__timeFilter(event_time) "
        "ORDER BY event_time DESC LIMIT 50")]), 12, 9)
    b.add(table("Что выбирают", [sql(
        "SELECT JSONExtractString(props, 'component') AS `компонент`, count() AS `выборов`, "
        "round(avg(JSONExtractInt(props, 'price_rub'))) AS `средняя цена, ₽` "
        "FROM analytics.events WHERE event = 'option_selected' AND $__timeFilter(event_time) "
        "GROUP BY `компонент` ORDER BY `выборов` DESC")]), 12, 9)
    return b


SVC = 'container_label_com_docker_compose_service'


def host():
    b = Board("horkilion-host", "Horkilion: сервер и контейнеры",
              "Ресурсы сервера (node-exporter) и потребление каждого контейнера (cAdvisor).", ["horkilion"])
    b.row("Сервер")
    b.add(stat("CPU", [prom(f'100 * (1 - avg(rate(node_cpu_seconds_total{{mode="idle"}}{RI})))')],
               unit="percent", thresholds=PCT_STEPS, decimals=0), 4, 4)
    b.add(stat("Память", [prom('100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)')],
               unit="percent", thresholds=PCT_STEPS, decimals=0), 4, 4)
    b.add(stat("Диск /", [prom(
        '100 * (1 - node_filesystem_avail_bytes{mountpoint="/", fstype!="rootfs"} '
        '/ node_filesystem_size_bytes{mountpoint="/", fstype!="rootfs"})')],
        unit="percent", thresholds=PCT_STEPS, decimals=0), 4, 4)
    b.add(stat("Свободно на диске", [prom('node_filesystem_avail_bytes{mountpoint="/", fstype!="rootfs"}')],
               unit="bytes", decimals=1), 4, 4)
    b.add(stat("Нагрузка (1 мин)", [prom('node_load1')], decimals=2,
               desc="Сравнивайте с числом ядер сервера."), 4, 4)
    b.add(stat("Аптайм", [prom('node_time_seconds - node_boot_time_seconds')], unit="s", decimals=0), 4, 4)
    b.add(timeseries("CPU по режимам", [prom(
        f'sum by (mode) (rate(node_cpu_seconds_total{{mode!="idle"}}{RI}))', "{{mode}}")],
        unit="short", stack=True, fill=40, desc="В ядрах."), 8, 8)
    b.add(timeseries("Память", refs([
        prom('node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes', "занято"),
        prom('node_memory_MemAvailable_bytes', "доступно")]), unit="bytes", stack=True, fill=40), 8, 8)
    b.add(timeseries("Сеть", refs([
        prom(f'sum(rate(node_network_receive_bytes_total{{device!~"lo|veth.*|br-.*|docker.*"}}{RI})) * 8', "входящий"),
        prom(f'-sum(rate(node_network_transmit_bytes_total{{device!~"lo|veth.*|br-.*|docker.*"}}{RI})) * 8',
             "исходящий")]), unit="bps"), 8, 8)

    b.row("Контейнеры")
    b.add(timeseries("CPU по сервисам", [prom(
        f'sum by ({SVC}) (rate(container_cpu_usage_seconds_total{{{SVC}!=""}}{RI}))', f"{{{{{SVC}}}}}")],
        unit="short", stack=True, fill=30, desc="В ядрах."), 12, 9)
    b.add(timeseries("Память по сервисам", [prom(
        f'sum by ({SVC}) (container_memory_working_set_bytes{{{SVC}!=""}})', f"{{{{{SVC}}}}}")],
        unit="bytes", stack=True, fill=30, desc="Рабочий набор: то, что ядро не может отобрать без OOM."), 12, 9)
    b.add(bargauge("Сейчас едят памяти", [prom(
        f'sort_desc(sum by ({SVC}) (container_memory_working_set_bytes{{{SVC}!=""}}))', f"{{{{{SVC}}}}}",
        instant=True)], unit="bytes"), 12, 10)
    b.add(timeseries("Сеть по сервисам", [prom(
        f'sum by ({SVC}) (rate(container_network_receive_bytes_total{{{SVC}!=""}}{RI})) * 8', f"{{{{{SVC}}}}}")],
        unit="bps", desc="Входящий трафик контейнера."), 12, 10)
    return b


def main():
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    for name, board in [("services", services()), ("agent", agent()), ("funnel", funnel()), ("host", host())]:
        refresh, time_from = ("1m", "now-7d") if name == "funnel" else ("30s", "now-6h")
        (out / f"{name}.json").write_text(json.dumps(board.json(refresh, time_from), ensure_ascii=False, indent=2) + "\n")
        print(name, len(board.panels), "panels")


main()
