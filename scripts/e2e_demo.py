#!/usr/bin/env python3
"""E2E Demo - Demonstracao de Exactly-Once do Nexus Event Gateway.

Envia N planos completos via Kafka, aguarda processamento e valida
que o Postgres tem exatamente os registros esperados.

Uso:
    python scripts/e2e_demo.py              # 10 planos (default)
    python scripts/e2e_demo.py --plans 100  # 100 planos
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import uuid
from datetime import datetime, timezone

import psycopg2
from confluent_kafka import Producer

# --- Config ---

KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP_SERVERS", "localhost:9092")
KAFKA_TOPIC = os.getenv("KAFKA_TOPIC", "orders")
POSTGRES_DSN = os.getenv(
    "POSTGRES_DSN",
    "dbname=nexus_db user=nexus_user password=nexus_pass host=localhost port=5432",
)

EVENT_SEQUENCE = [
    ("OrderCreated", {"user_id": "demo-user", "items": [{"product_id": "p1", "quantity": 1, "unit_price": 50.0}], "total_amount": 50.0, "currency": "BRL"}),
    ("InventoryValidated", {"warehouse_id": "wh_demo", "all_items_available": True, "reserved_until": "2026-12-31T00:00:00Z"}),
    ("PaymentProcessed", {"payment_id": "pay_demo", "method": "credit_card", "amount": 50.0, "status": "approved"}),
    ("OrderShipped", {"tracking_code": "TRK_DEMO", "carrier": "correios", "estimated_delivery": "2026-12-31T00:00:00Z"}),
    ("OrderCompleted", {"completed_at": "2026-03-05T00:00:00Z", "final_status": "delivered"}),
]

EXPECTED_FINAL_STATUS = "completed"


def make_event(plan_id: str, order_id: str, event_type: str, seq_id: int, data: dict) -> dict:
    return {
        "event_id": str(uuid.uuid4()),
        "event_type": event_type,
        "plan_id": plan_id,
        "seq_id": seq_id,
        "order_id": order_id,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "schema_version": "1.0",
        "data": data,
    }


def delivery_report(err, msg):
    if err:
        print(f"  ERRO ao enviar: {err}", file=sys.stderr)


def send_plans(producer: Producer, num_plans: int) -> list[tuple[str, str]]:
    """Envia N planos completos ao Kafka. Retorna lista de (plan_id, order_id)."""
    plans = []
    for i in range(num_plans):
        plan_id = f"plan_{uuid.uuid4().hex[:16]}"
        order_id = str(uuid.uuid4())
        plans.append((plan_id, order_id))

        for seq, (event_type, data) in enumerate(EVENT_SEQUENCE, start=1):
            event = make_event(plan_id, order_id, event_type, seq, data)
            producer.produce(
                KAFKA_TOPIC,
                key=plan_id,
                value=json.dumps(event).encode(),
                callback=delivery_report,
            )
        producer.poll(0)

    producer.flush(timeout=30)
    print(f"[OK] {num_plans} planos enviados ({num_plans * len(EVENT_SEQUENCE)} eventos)")
    return plans


def wait_and_verify(plans: list[tuple[str, str]], timeout: int = 60) -> bool:
    """Espera processamento e valida Postgres."""
    print(f"\nAguardando processamento (timeout: {timeout}s)...")

    conn = psycopg2.connect(POSTGRES_DSN)
    conn.autocommit = True
    cur = conn.cursor()

    order_ids = [oid for _, oid in plans]
    start = time.time()
    completed = 0

    while time.time() - start < timeout:
        placeholders = ",".join(["%s"] * len(order_ids))
        cur.execute(
            f"SELECT COUNT(*) FROM orders WHERE id IN ({placeholders}) AND status = 'completed' AND last_seq_processed = 5",
            order_ids,
        )
        completed = cur.fetchone()[0]

        if completed == len(plans):
            break
        time.sleep(1)

    elapsed = time.time() - start

    # --- Relatorio ---
    print(f"\n{'='*60}")
    print(f"  RELATORIO E2E DEMO - Nexus Event Gateway")
    print(f"{'='*60}")
    print(f"  Planos enviados:      {len(plans)}")
    print(f"  Eventos totais:       {len(plans) * len(EVENT_SEQUENCE)}")
    print(f"  Planos completados:   {completed}/{len(plans)}")
    print(f"  Tempo de processamento: {elapsed:.1f}s")
    print(f"  Latencia media/plano: {elapsed / max(len(plans), 1) * 1000:.0f}ms")

    # Check for duplicates via outbox
    cur.execute(
        f"SELECT aggregate_id, event_type, COUNT(*) FROM outbox WHERE aggregate_id IN ({placeholders}) GROUP BY aggregate_id, event_type HAVING COUNT(*) > 1",
        order_ids,
    )
    duplicates = cur.fetchall()

    # Check for missing orders
    cur.execute(
        f"SELECT id, status, last_seq_processed FROM orders WHERE id IN ({placeholders}) AND (status != 'completed' OR last_seq_processed != 5)",
        order_ids,
    )
    incomplete = cur.fetchall()

    if duplicates:
        print(f"\n  DUPLICATAS NO OUTBOX: {len(duplicates)}")
        for d in duplicates[:5]:
            print(f"    order={d[0]}, type={d[1]}, count={d[2]}")
    else:
        print(f"  Duplicatas no outbox: 0")

    if incomplete:
        print(f"\n  PEDIDOS INCOMPLETOS: {len(incomplete)}")
        for inc in incomplete[:5]:
            print(f"    order={inc[0]}, status={inc[1]}, last_seq={inc[2]}")
    else:
        print(f"  Pedidos incompletos:  0")

    success = completed == len(plans) and not duplicates
    print(f"\n  {'RESULTADO: EXACTLY-ONCE VALIDADO' if success else 'RESULTADO: FALHA - VERIFICAR LOGS'}")
    print(f"{'='*60}\n")

    cur.close()
    conn.close()
    return success


def main():
    parser = argparse.ArgumentParser(description="Nexus E2E Demo")
    parser.add_argument("--plans", type=int, default=10, help="Numero de planos a enviar")
    parser.add_argument("--timeout", type=int, default=60, help="Timeout de verificacao (s)")
    args = parser.parse_args()

    print(f"Nexus Event Gateway - E2E Demo")
    print(f"Kafka: {KAFKA_BOOTSTRAP} | Topic: {KAFKA_TOPIC}")
    print(f"Planos: {args.plans}\n")

    producer = Producer({"bootstrap.servers": KAFKA_BOOTSTRAP})

    plans = send_plans(producer, args.plans)
    success = wait_and_verify(plans, timeout=args.timeout)

    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
