#!/usr/bin/env python3
"""Chaos Test - Validação de Exactly-Once e resiliência do Nexus Event Gateway.

Oráculo: envia N pedidos via Kafka, introduz caos (sequence gaps, Redis restart,
mensagens envenenadas),
e valida que o Postgres tem exatamente os registros esperados com status correto.

Requisitos:
    pip install confluent-kafka psycopg2-binary docker

Uso:
    python scripts/chaos_test.py                  # Roda todos os cenários
    python scripts/chaos_test.py --scenario gaps  # Apenas sequence gaps
    python scripts/chaos_test.py --scenario redis # Apenas Redis restart
    python scripts/chaos_test.py --scenario poison # Apenas mensagens envenenadas (DLQ)
    python scripts/chaos_test.py --orders 50      # 50 pedidos por cenário
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone, timedelta

import docker
import psycopg2
from confluent_kafka import Consumer, KafkaException, Producer

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("chaos-test")

# --- Configuração ---

KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP_SERVERS", "localhost:9092")
KAFKA_TOPIC = os.getenv("KAFKA_TOPIC", "orders")
KAFKA_DLQ_TOPIC = os.getenv("KAFKA_DLQ_TOPIC", "orders-dlq")
POSTGRES_DSN = os.getenv(
    "POSTGRES_DSN",
    "dbname=nexus_db user=nexus_user password=nexus_pass host=localhost port=5432",
)
DOCKER_REDIS_CONTAINER = os.getenv("REDIS_CONTAINER", "nexus-redis")
DOCKER_SERVER_CONTAINER = os.getenv("SERVER_CONTAINER", "nexus-server")

# Sequência completa de event types para um pedido happy-path
EVENT_SEQUENCE = [
    "OrderCreated",
    "InventoryValidated",
    "PaymentProcessed",
    "OrderShipped",
    "OrderCompleted",
]

STATUS_MAP = {
    "OrderCreated": "pending",
    "InventoryValidated": "inventory_validated",
    "PaymentProcessed": "payment_processed",
    "OrderShipped": "shipped",
    "OrderCompleted": "completed",
}


# --- Helpers ---


def _make_event(plan_id: str, order_id: str, event_type: str, seq_id: int) -> dict:
    """Gera um EventEnvelope mínimo para o Kafka."""
    data: dict = {}
    if event_type == "OrderCreated":
        data = {
            "user_id": "chaos_user",
            "items": [{"product_id": "p1", "quantity": 1, "unit_price": 10.0}],
            "total_amount": 10.0,
            "currency": "BRL",
        }
    elif event_type == "InventoryValidated":
        data = {
            "warehouse_id": "wh_test",
            "all_items_available": True,
            "reserved_until": (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat(),
        }
    elif event_type == "PaymentProcessed":
        data = {
            "payment_id": f"pay_{uuid.uuid4().hex[:8]}",
            "method": "credit_card",
            "amount": 10.0,
            "status": "approved",
        }
    elif event_type == "OrderShipped":
        data = {
            "tracking_code": f"TRK{uuid.uuid4().hex[:10].upper()}",
            "carrier": "correios",
            "estimated_delivery": (datetime.now(timezone.utc) + timedelta(days=5)).isoformat(),
        }
    elif event_type == "OrderCompleted":
        data = {
            "completed_at": datetime.now(timezone.utc).isoformat(),
            "final_status": "delivered",
        }

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


def _make_abort_event(plan_id: str, order_id: str) -> dict:
    """Gera ABORT_PLAN tombstone."""
    return {
        "event_id": str(uuid.uuid4()),
        "event_type": "ABORT_PLAN",
        "plan_id": plan_id,
        "order_id": order_id,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "schema_version": "1.0",
        "data": {
            "reason": "Chaos test abort",
            "abort_code": "manual",
            "aborted_at_seq": 0,
        },
    }


@dataclass
class Plan:
    plan_id: str = field(default_factory=lambda: f"plan_{uuid.uuid4().hex[:16]}")
    order_id: str = field(default_factory=lambda: str(uuid.uuid4()))


def _produce(producer: Producer, events: list[dict]) -> None:
    """Envia lista de eventos para o Kafka com ordering por order_id."""
    for event in events:
        producer.produce(
            topic=KAFKA_TOPIC,
            key=event["order_id"],
            value=json.dumps(event).encode(),
            callback=lambda err, msg: (
                logger.error("Delivery failed: %s", err) if err else None
            ),
        )
    producer.flush(timeout=15)


def _wait_for_processing(conn, expected_count: int, timeout: int = 30) -> bool:
    """Espera até que o Postgres tenha pelo menos expected_count orders processados."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        with conn.cursor() as cur:
            cur.execute("SELECT COUNT(*) FROM orders")
            count = cur.fetchone()[0]
            if count >= expected_count:
                return True
        time.sleep(1)
    return False


def _health_check() -> bool:
    """Verifica se a infra básica está acessível."""
    ok = True
    # Kafka
    try:
        p = Producer({"bootstrap.servers": KAFKA_BOOTSTRAP})
        meta = p.list_topics(timeout=5)
        logger.info("Kafka OK: %d brokers", len(meta.brokers))
    except KafkaException as e:
        logger.error("Kafka inacessível: %s", e)
        ok = False

    # Postgres
    try:
        conn = psycopg2.connect(POSTGRES_DSN)
        conn.close()
        logger.info("Postgres OK")
    except Exception as e:
        logger.error("Postgres inacessível: %s", e)
        ok = False

    # Docker
    try:
        client = docker.from_env()
        client.ping()
        logger.info("Docker OK")
    except Exception as e:
        logger.error("Docker inacessível: %s", e)
        ok = False

    return ok


def _clean_db(conn) -> None:
    """Limpa tabelas para começar teste do zero."""
    with conn.cursor() as cur:
        cur.execute("DELETE FROM outbox")
        cur.execute("DELETE FROM pending_events")
        cur.execute("DELETE FROM orders")
    conn.commit()
    logger.info("DB limpo")


# --- Cenários de Caos ---


@dataclass
class ScenarioResult:
    name: str
    passed: bool
    details: str


def scenario_sequence_gaps(producer: Producer, conn, num_orders: int) -> ScenarioResult:
    """Cenário: envia eventos com gaps de sequência.

    Para cada pedido, envia seq 1, 3, 4, 5 (pula 2).
    Depois envia o seq 2 faltante.
    Valida que todos os pedidos terminam com status 'completed'.
    """
    name = "Sequence Gaps"
    logger.info("=== Cenário: %s (%d pedidos) ===", name, num_orders)

    _clean_db(conn)
    plans = [Plan() for _ in range(num_orders)]

    # Fase 1: enviar eventos com gap (pula seq 2)
    for p in plans:
        events = []
        for i, evt_type in enumerate(EVENT_SEQUENCE):
            seq = i + 1
            if seq == 2:
                continue  # Pula InventoryValidated
            events.append(_make_event(p.plan_id, p.order_id, evt_type, seq))
        _produce(producer, events)

    logger.info("Fase 1: eventos com gap enviados, aguardando buffering (3s)...")
    time.sleep(3)

    # Fase 2: enviar o evento faltante (seq 2)
    for p in plans:
        event = _make_event(p.plan_id, p.order_id, "InventoryValidated", 2)
        _produce(producer, [event])

    logger.info("Fase 2: eventos faltantes enviados, aguardando processamento...")

    if not _wait_for_processing(conn, num_orders, timeout=30):
        return ScenarioResult(name, False, "Timeout esperando processamento")

    # Validação: todos devem ter status 'completed' e last_seq=5
    time.sleep(3)  # buffer extra para drain
    with conn.cursor() as cur:
        cur.execute(
            "SELECT status, last_seq_processed FROM orders WHERE plan_id = ANY(%s)",
            ([p.plan_id for p in plans],),
        )
        rows = cur.fetchall()

    if len(rows) != num_orders:
        return ScenarioResult(
            name, False,
            f"Esperado {num_orders} orders, encontrado {len(rows)}"
        )

    failed = [(s, seq) for s, seq in rows if s != "completed" or seq != 5]
    if failed:
        return ScenarioResult(
            name, False,
            f"{len(failed)} orders com estado incorreto. Ex: {failed[:3]}"
        )

    return ScenarioResult(name, True, f"{num_orders} orders 'completed' com seq=5")


def _wait_for_completion(conn, plans: list[Plan], timeout: int) -> int:
    """Espera todos os planos chegarem a 'completed' com seq 5. Retorna quantos chegaram."""
    plan_ids = [p.plan_id for p in plans]
    deadline = time.time() + timeout
    completed = 0
    while time.time() < deadline:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT COUNT(*) FROM orders WHERE plan_id = ANY(%s) "
                "AND status = 'completed' AND last_seq_processed = 5",
                (plan_ids,),
            )
            completed = cur.fetchone()[0]
        if completed == len(plans):
            break
        time.sleep(1)
    return completed


def scenario_redis_restart(producer: Producer, conn, num_orders: int) -> ScenarioResult:
    """Cenário: derruba o Redis no meio dos planos.

    Envia os 2 primeiros eventos de cada plano, derruba o Redis, envia os 3
    restantes com o Redis fora e sobe o Redis de volta. Nada é reenviado: o
    sistema precisa terminar todos os pedidos sozinho, com cada evento
    aplicado exatamente uma vez.
    """
    name = "Redis Restart"
    logger.info("=== Cenário: %s (%d pedidos) ===", name, num_orders)

    _clean_db(conn)
    plans = [Plan() for _ in range(num_orders)]
    docker_client = docker.from_env()

    # Fase 1: primeiros 2 eventos de cada plano com o Redis de pé
    for p in plans:
        _produce(producer, [
            _make_event(p.plan_id, p.order_id, EVENT_SEQUENCE[i], i + 1)
            for i in range(2)
        ])

    logger.info("Fase 1: primeiros eventos enviados, aguardando processamento (3s)...")
    time.sleep(3)

    # Fase 2: derrubar Redis
    try:
        redis_container = docker_client.containers.get(DOCKER_REDIS_CONTAINER)
        logger.info("Derrubando Redis...")
        redis_container.stop(timeout=2)
    except Exception as e:
        return ScenarioResult(name, False, f"Falha ao derrubar Redis: {e}")

    # Fase 3: eventos restantes com o Redis fora do ar
    try:
        for p in plans:
            _produce(producer, [
                _make_event(p.plan_id, p.order_id, EVENT_SEQUENCE[i], i + 1)
                for i in range(2, 5)
            ])
        logger.info("Fase 3: eventos enviados com Redis offline, aguardando (3s)...")
        time.sleep(3)
    finally:
        # Fase 4: subir Redis de volta mesmo se a fase 3 falhar
        redis_container.start()
        logger.info("Redis reiniciado")

    completed = _wait_for_completion(conn, plans, timeout=45)

    with conn.cursor() as cur:
        # Cada evento deve gerar exatamente uma notificação no outbox
        cur.execute(
            """SELECT o.aggregate_id, o.event_type, COUNT(*) FROM outbox o
               JOIN orders ord ON ord.id = o.aggregate_id
               WHERE ord.plan_id = ANY(%s)
               GROUP BY o.aggregate_id, o.event_type HAVING COUNT(*) > 1""",
            ([p.plan_id for p in plans],),
        )
        duplicates = cur.fetchall()

        cur.execute(
            """SELECT COUNT(*) FROM outbox o JOIN orders ord ON ord.id = o.aggregate_id
               WHERE ord.plan_id = ANY(%s)""",
            ([p.plan_id for p in plans],),
        )
        notifications = cur.fetchone()[0]

    if duplicates:
        return ScenarioResult(
            name, False,
            f"DUPLICATAS no outbox: {len(duplicates)} eventos notificados mais de uma vez. Ex: {duplicates[:3]}"
        )
    if completed != num_orders:
        return ScenarioResult(
            name, False,
            f"Apenas {completed}/{num_orders} pedidos 'completed' com seq=5 (sem reenvio)"
        )
    if notifications != num_orders * len(EVENT_SEQUENCE):
        return ScenarioResult(
            name, False,
            f"{notifications} notificações no outbox, esperado {num_orders * len(EVENT_SEQUENCE)}"
        )

    return ScenarioResult(
        name, True,
        f"{num_orders} pedidos 'completed' com seq=5 sem reenvio, "
        f"{notifications} notificações, 0 duplicatas"
    )


def scenario_zombie_events(producer: Producer, conn, num_orders: int) -> ScenarioResult:
    """Cenário: envia eventos para um plan_id que já foi abortado.

    Envia ABORT_PLAN primeiro, depois eventos normais.
    Valida que os eventos zombie são descartados.
    """
    name = "Zombie Events"
    logger.info("=== Cenário: %s (%d pedidos) ===", name, num_orders)

    _clean_db(conn)
    plans = [Plan() for _ in range(num_orders)]

    # Fase 1: enviar evento OrderCreated + ABORT_PLAN para cada plano
    for p in plans:
        events = [
            _make_event(p.plan_id, p.order_id, "OrderCreated", 1),
            _make_abort_event(p.plan_id, p.order_id),
        ]
        _produce(producer, events)

    logger.info("Fase 1: OrderCreated + ABORT enviados, aguardando (3s)...")
    time.sleep(3)

    # Fase 2: enviar eventos zombie (para planos já abortados)
    for p in plans:
        events = [
            _make_event(p.plan_id, p.order_id, EVENT_SEQUENCE[i], i + 1)
            for i in range(1, 5)
        ]
        _produce(producer, events)

    logger.info("Fase 2: zombie events enviados, aguardando (5s)...")
    time.sleep(5)

    # Validação: todos devem ter status 'aborted'
    with conn.cursor() as cur:
        cur.execute(
            "SELECT status, last_seq_processed FROM orders WHERE plan_id = ANY(%s)",
            ([p.plan_id for p in plans],),
        )
        rows = cur.fetchall()

    if len(rows) != num_orders:
        return ScenarioResult(
            name, False,
            f"Esperado {num_orders} orders, encontrado {len(rows)}"
        )

    non_aborted = [(s, seq) for s, seq in rows if s != "aborted"]
    if non_aborted:
        return ScenarioResult(
            name, False,
            f"{len(non_aborted)} orders NÃO abortados (zombies processados!). Ex: {non_aborted[:3]}"
        )

    return ScenarioResult(name, True, f"{num_orders} orders abortados, 0 zombies processados")


def _count_dlq_markers(marker: str, timeout: int = 20) -> int:
    """Conta mensagens na DLQ cujo payload contém marker."""
    consumer = Consumer({
        "bootstrap.servers": KAFKA_BOOTSTRAP,
        "group.id": f"chaos-dlq-reader-{uuid.uuid4().hex[:8]}",
        "auto.offset.reset": "earliest",
        "enable.auto.commit": False,
    })
    consumer.subscribe([KAFKA_DLQ_TOPIC])
    found = 0
    deadline = time.time() + timeout
    idle_since = None
    try:
        while time.time() < deadline:
            msg = consumer.poll(1.0)
            if msg is None:
                # Depois de achar algo, 3s sem mensagens novas = fim do tópico
                if found and idle_since and time.time() - idle_since > 3:
                    break
                idle_since = idle_since or time.time()
                continue
            idle_since = None
            if msg.error() is None and marker.encode() in msg.value():
                found += 1
    finally:
        consumer.close()
    return found


def scenario_poison_messages(producer: Producer, conn, num_orders: int) -> ScenarioResult:
    """Cenário: mensagens que nunca podem ser processadas no meio de planos válidos.

    Envia JSON malformado e eventos sem seq_id intercalados com planos
    completos. As mensagens envenenadas devem ir para a DLQ sem travar a
    partição, e todos os planos válidos devem terminar.
    """
    name = "Poison Messages"
    logger.info("=== Cenário: %s (%d pedidos) ===", name, num_orders)

    _clean_db(conn)
    marker = f"chaos-poison-{uuid.uuid4().hex[:12]}"
    plans = [Plan() for _ in range(num_orders)]

    for i, p in enumerate(plans):
        # Mesmo order_id do plano válido: cai na mesma partição e seria
        # bloqueado se a mensagem envenenada travasse o consumer.
        producer.produce(KAFKA_TOPIC, key=p.order_id, value=f'{{"broken": "{marker}"'.encode())
        invalid = _make_event(f"{marker}-{i}", p.order_id, "OrderCreated", 1)
        del invalid["seq_id"]
        producer.produce(KAFKA_TOPIC, key=p.order_id, value=json.dumps(invalid).encode())
        _produce(producer, [
            _make_event(p.plan_id, p.order_id, evt, seq)
            for seq, evt in enumerate(EVENT_SEQUENCE, start=1)
        ])

    completed = _wait_for_completion(conn, plans, timeout=45)
    if completed != num_orders:
        return ScenarioResult(
            name, False,
            f"Apenas {completed}/{num_orders} planos válidos 'completed' (partição travada?)"
        )

    expected_dlq = num_orders * 2
    in_dlq = _count_dlq_markers(marker)
    if in_dlq != expected_dlq:
        return ScenarioResult(
            name, False,
            f"{in_dlq} mensagens envenenadas na DLQ, esperado {expected_dlq}"
        )

    return ScenarioResult(
        name, True,
        f"{num_orders} planos válidos 'completed', {in_dlq} mensagens envenenadas na DLQ"
    )


# --- Main ---


def main() -> None:
    parser = argparse.ArgumentParser(description="Nexus Chaos Test Suite")
    parser.add_argument(
        "--scenario",
        choices=["gaps", "redis", "zombie", "poison", "all"],
        default="all",
        help="Cenário a executar (default: all)",
    )
    parser.add_argument(
        "--orders",
        type=int,
        default=10,
        help="Número de pedidos por cenário (default: 10)",
    )
    args = parser.parse_args()

    logger.info("=" * 60)
    logger.info("NEXUS CHAOS TEST - The Gauntlet")
    logger.info("=" * 60)

    # Health check
    if not _health_check():
        logger.error("Pré-condições falharam. Suba a infra com 'make up' e tente novamente.")
        sys.exit(1)

    producer = Producer({
        "bootstrap.servers": KAFKA_BOOTSTRAP,
        "acks": "all",
        "enable.idempotence": True,
        "retries": 5,
        "max.in.flight.requests.per.connection": 1,
    })
    conn = psycopg2.connect(POSTGRES_DSN)
    conn.autocommit = True

    results: list[ScenarioResult] = []

    scenarios = {
        "gaps": scenario_sequence_gaps,
        "redis": scenario_redis_restart,
        "zombie": scenario_zombie_events,
        "poison": scenario_poison_messages,
    }

    to_run = list(scenarios.keys()) if args.scenario == "all" else [args.scenario]

    for name in to_run:
        try:
            result = scenarios[name](producer, conn, args.orders)
            results.append(result)
        except Exception as e:
            logger.exception("Cenário '%s' falhou com exceção", name)
            results.append(ScenarioResult(name, False, f"Exceção: {e}"))

    conn.close()

    # Relatório final
    logger.info("")
    logger.info("=" * 60)
    logger.info("RELATÓRIO FINAL")
    logger.info("=" * 60)

    all_passed = True
    for r in results:
        icon = "PASS" if r.passed else "FAIL"
        logger.info("[%s] %s: %s", icon, r.name, r.details)
        if not r.passed:
            all_passed = False

    logger.info("-" * 60)
    if all_passed:
        logger.info("Consistency: OK - Todos os cenários passaram!")
    else:
        logger.info("Consistency: FAILED - Alguns cenários falharam")

    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
