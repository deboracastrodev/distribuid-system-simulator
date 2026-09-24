#!/usr/bin/env python3
"""Confere, pelo relatório do agent, que o server chegou ao estado esperado.

O agent (python -m src.main --report ...) grava o que publicou em cada plano:
completo, ou abortado por estoque/pagamento depois de N eventos. Para cada
plano, este script espera até que:

1. orders tenha o pedido com o plan_id do plano e
   - completo:  status 'completed', last_seq_processed = 5;
   - abortado:  status 'aborted', last_seq_processed = último seq publicado
                (0 = tombstone: ABORT_PLAN chegou antes de qualquer evento);
2. o outbox tenha exatamente um registro por evento aplicado (o ABORT_PLAN de
   um pedido já iniciado também notifica; o tombstone não), todos entregues;
3. o webhook sink tenha aceito cada entrega uma vez e em ordem.

Também exige que cada tipo de desfecho com taxa > 0 na simulação tenha
acontecido: com poucos planos ou uma seed azarada o teste não provaria nada.

Uso:
    python scripts/check_agent_outcomes.py agent-report.json
    python scripts/check_agent_outcomes.py agent-report.json --skip-sink
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.request
from collections import Counter

import psycopg2

POSTGRES_DSN = os.getenv(
    "POSTGRES_DSN",
    "dbname=nexus_db user=nexus_user password=nexus_pass host=localhost port=5432",
)
SINK_URL = os.getenv("WEBHOOK_SINK_URL", "http://localhost:9090")

# Taxa da simulação -> abort_reason que ela produz.
FAILURE_KINDS = {
    "inventory_failure_rate": "inventory_failed",
    "payment_rejection_rate": "payment_rejected",
}


def expected(plan: dict) -> tuple[str, int, list[str]]:
    """(status, last_seq_processed, event_types do outbox) esperados para o plano."""
    if plan["outcome"] == "completed":
        return "completed", 5, plan["events"]
    if plan["last_seq"] == 0:
        return "aborted", 0, []  # tombstone: nada a notificar
    return "aborted", plan["last_seq"], plan["events"]


def db_problems(cur, plan: dict) -> list[str]:
    status, last_seq, events = expected(plan)
    order_id = plan["order_id"]

    cur.execute("SELECT status, last_seq_processed, plan_id FROM orders WHERE id = %s", (order_id,))
    row = cur.fetchone()
    if row is None:
        return [f"{order_id}: pedido ausente em orders"]
    if row != (status, last_seq, plan["plan_id"]):
        return [f"{order_id}: orders = {row}, esperado {(status, last_seq, plan['plan_id'])}"]

    cur.execute(
        "SELECT event_type, processed, dead_at IS NOT NULL FROM outbox WHERE aggregate_id = %s ORDER BY position",
        (order_id,),
    )
    rows = cur.fetchall()
    types = [r[0] for r in rows]
    if types != events:
        return [f"{order_id}: outbox = {types}, esperado {events}"]
    if any(dead for _, _, dead in rows):
        return [f"{order_id}: entrada do outbox em dead-letter"]
    if not all(processed for _, processed, _ in rows):
        return [f"{order_id}: outbox com entregas pendentes"]
    return []


def sink_problems(stats: dict, plan: dict) -> list[str]:
    _, _, events = expected(plan)
    agg = stats.get(plan["order_id"], {})
    problems = []
    if agg.get("accepted", 0) != len(events):
        problems.append(f"{plan['order_id']}: sink aceitou {agg.get('accepted', 0)} entregas, esperado {len(events)}")
    for counter in ("duplicates", "out_of_order", "missing_key"):
        if agg.get(counter, 0):
            problems.append(f"{plan['order_id']}: sink registrou {counter} = {agg[counter]}")
    return problems


def fetch_sink_stats() -> dict:
    with urllib.request.urlopen(f"{SINK_URL}/stats", timeout=10) as resp:
        return json.loads(resp.read())["aggregates"]


def coverage_problems(report: dict) -> list[str]:
    plans = report["plans"]
    reasons = Counter(p["abort_reason"] for p in plans if p["outcome"] == "aborted")
    problems = []
    if not any(p["outcome"] == "completed" for p in plans):
        problems.append("nenhum plano completo")
    for rate_key, reason in FAILURE_KINDS.items():
        if report["simulation"].get(rate_key, 0) > 0 and not reasons[reason]:
            problems.append(f"{rate_key} > 0, mas nenhum plano abortou com {reason}")
    return problems


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("report", help="relatório gravado pelo agent com --report")
    parser.add_argument("--timeout", type=int, default=90, help="espera máxima, em segundos")
    parser.add_argument("--skip-sink", action="store_true", help="não confere o webhook sink")
    args = parser.parse_args()

    with open(args.report) as f:
        report = json.load(f)
    plans = report["plans"]
    outcomes = Counter(p["abort_reason"] or "completed" for p in plans)
    print(f"{len(plans)} planos no relatório: {dict(sorted(outcomes.items()))}")

    failed = False
    problems = coverage_problems(report)
    print(f"[{'FAIL' if problems else 'PASS'}] desfechos simulados")
    for p in problems:
        print(f"    - {p}")
    failed |= bool(problems)

    conn = psycopg2.connect(POSTGRES_DSN)
    conn.autocommit = True
    cur = conn.cursor()
    checks = [("estado no Postgres e entrega do outbox", lambda: [x for p in plans for x in db_problems(cur, p)])]
    if not args.skip_sink:
        def check_sink() -> list[str]:
            stats = fetch_sink_stats()
            return [x for p in plans for x in sink_problems(stats, p)]
        checks.append(("entregas no webhook sink", check_sink))

    for name, check in checks:
        deadline = time.time() + args.timeout
        while (problems := check()) and time.time() < deadline:
            time.sleep(1)
        print(f"[{'FAIL' if problems else 'PASS'}] {name}")
        for p in problems[:20]:
            print(f"    - {p}")
        if len(problems) > 20:
            print(f"    ... e mais {len(problems) - 20}")
        failed |= bool(problems)

    conn.close()
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
