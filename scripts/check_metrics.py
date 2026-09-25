#!/usr/bin/env python3
"""Valida a cadeia de métricas: server -> Prometheus -> dashboard do Grafana.

Roda depois do e2e e do chaos test (é o que o CI faz), porque espera
encontrar os efeitos deles: eventos aplicados, mensagens na DLQ, webhooks
entregues, retentados e rejeitados.

Verifica:
1. /metrics do server expõe todas as séries esperadas;
2. o Prometheus está fazendo scrape do server (up == 1) e os contadores
   cresceram de forma coerente com o que rodou. O valor vem do Prometheus
   (increase), e não do /metrics: o cenário de crash reinicia o server e zera
   os contadores em memória, e o increase() trata esse reinício;
3. toda query do dashboard executa sem erro e retorna pelo menos uma série;
4. o Grafana provisionou o dashboard e o datasource Prometheus responde.

Uso:
    python scripts/check_metrics.py                 # tudo
    python scripts/check_metrics.py --skip-grafana  # sem Grafana
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

SERVER_METRICS_URL = os.getenv("SERVER_METRICS_URL", "http://localhost:8080/metrics")
PROMETHEUS_URL = os.getenv("PROMETHEUS_URL", "http://localhost:9095")
GRAFANA_URL = os.getenv("GRAFANA_URL", "http://localhost:3000")
GRAFANA_AUTH = os.getenv("GRAFANA_AUTH", "admin:nexus")
DASHBOARD = Path(__file__).resolve().parent.parent / "monitoring/grafana/dashboards/nexus-metrics.json"

# (série, rótulos) que o e2e, o agent e o chaos test garantem ter crescido.
EXPECTED_POSITIVE = [
    ("nexus_events_processed_total", {"kind": "sequenced", "outcome": "apply"}),
    ("nexus_events_processed_total", {"kind": "abort", "outcome": "apply"}),
    ("nexus_events_drained_total", {}),
    ("nexus_dlq_messages_total", {"code": "PARSE_ERROR"}),
    ("nexus_dlq_messages_total", {"code": "INVALID_EVENT"}),
    ("nexus_webhook_deliveries_total", {"result": "delivered"}),
    ("nexus_webhook_deliveries_total", {"result": "retry_scheduled"}),
    ("nexus_webhook_deliveries_total", {"result": "dead_rejected"}),
    ("nexus_event_settle_seconds_count", {}),
    ("nexus_webhook_request_seconds_count", {}),
]
# Séries que precisam existir (o valor pode ser 0).
EXPECTED_PRESENT = ["nexus_consumer_lag", "nexus_outbox_entries", "nexus_pending_events", "nexus_circuit_breaker_state"]

LINE = re.compile(r'^(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)(\{(?P<labels>.*)\})?\s+(?P<value>\S+)')
LABEL = re.compile(r'(\w+)="((?:[^"\\]|\\.)*)"')


def get(url: str, auth: str | None = None) -> bytes:
    req = urllib.request.Request(url)
    if auth:
        req.add_header("Authorization", "Basic " + base64.b64encode(auth.encode()).decode())
    with urllib.request.urlopen(req, timeout=10) as resp:
        return resp.read()


def parse_exposition(text: str) -> list[tuple[str, dict, float]]:
    samples = []
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        m = LINE.match(line)
        if m:
            labels = dict(LABEL.findall(m.group("labels") or ""))
            samples.append((m.group("name"), labels, float(m.group("value"))))
    return samples


def value(samples, name: str, labels: dict) -> float | None:
    for n, l, v in samples:
        if n == name and all(l.get(k) == val for k, val in labels.items()):
            return v
    return None


def prom_query(expr: str) -> list:
    url = f"{PROMETHEUS_URL}/api/v1/query?" + urllib.parse.urlencode({"query": expr})
    try:
        body = json.loads(get(url))
    except urllib.error.HTTPError as e:
        # O Prometheus explica o erro (ex.: de sintaxe PromQL) no corpo da resposta.
        body = json.loads(e.read() or b"{}")
    if body.get("status") != "success":
        raise RuntimeError(body.get("error", "query failed"))
    return body["data"]["result"]


def check_server() -> list[str]:
    samples = parse_exposition(get(SERVER_METRICS_URL).decode())
    problems = []
    for name, labels in EXPECTED_POSITIVE:
        if value(samples, name, labels) is None:
            problems.append(f"série ausente: {name}{labels}")
    for name in EXPECTED_PRESENT:
        if not any(n == name for n, _, _ in samples):
            problems.append(f"série ausente: {name}")
    return problems


def selector(name: str, labels: dict) -> str:
    return name + "{" + ",".join(f'{k}="{v}"' for k, v in labels.items()) + "}"


def not_increased() -> list[str]:
    problems = []
    for name, labels in EXPECTED_POSITIVE:
        result = prom_query(f"sum(increase({selector(name, labels)}[1h]))")
        total = float(result[0]["value"][1]) if result else 0.0
        if total <= 0:
            problems.append(f"{name}{labels} não cresceu na última hora (increase = {total})")
    return problems


def check_prometheus(timeout: int = 60) -> list[str]:
    deadline = time.time() + timeout
    problems = ["Prometheus não fez scrape do server (up != 1)"]
    while time.time() < deadline:
        up = prom_query('up{job="nexus-server"}')
        if up and float(up[0]["value"][1]) == 1:
            # O último incremento pode ainda não ter sido raspado: espera o próximo scrape.
            problems = not_increased()
            if not problems:
                return []
        time.sleep(2)
    return problems


def check_dashboard_queries() -> list[str]:
    dashboard = json.loads(DASHBOARD.read_text())
    problems = []
    for panel in dashboard["panels"]:
        for target in panel.get("targets", []):
            expr = target["expr"]
            try:
                if not prom_query(expr):
                    problems.append(f"painel '{panel['title']}': sem dados para {expr}")
            except Exception as e:  # noqa: BLE001 - reporta qualquer falha da query
                problems.append(f"painel '{panel['title']}': erro em {expr}: {e}")
    return problems


def check_grafana(timeout: int = 60) -> list[str]:
    uid = json.loads(DASHBOARD.read_text())["uid"]
    deadline = time.time() + timeout
    last_error = ""
    while time.time() < deadline:
        try:
            dash = json.loads(get(f"{GRAFANA_URL}/api/dashboards/uid/{uid}", GRAFANA_AUTH))
            health = json.loads(get(f"{GRAFANA_URL}/api/datasources/uid/prometheus/health", GRAFANA_AUTH))
            if dash["dashboard"]["uid"] == uid and health.get("status") == "OK":
                return []
            last_error = f"datasource: {health}"
        except Exception as e:  # noqa: BLE001
            last_error = str(e)
        time.sleep(2)
    return [f"Grafana: dashboard '{uid}' ou datasource Prometheus indisponível ({last_error})"]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--skip-grafana", action="store_true", help="não verifica o Grafana")
    args = parser.parse_args()

    checks = [
        ("server /metrics", check_server),
        ("scrape e contadores no Prometheus", check_prometheus),
        ("queries do dashboard", check_dashboard_queries),
    ]
    if not args.skip_grafana:
        checks.append(("provisionamento do Grafana", check_grafana))

    failed = False
    for name, check in checks:
        problems = check()
        print(f"[{'FAIL' if problems else 'PASS'}] {name}")
        for p in problems:
            print(f"    - {p}")
        failed |= bool(problems)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
