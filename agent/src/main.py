"""Entrypoint do Nexus Agent Planner.

Modo CLI: recebe pedido via --order JSON, executa o plano e publica no Kafka.
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
import uuid

from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter

from src.config import OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_SERVICE_NAME
from src.planner.graph import build_graph
from src.producer.kafka_producer import create_producer, publish_plan

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)


def setup_tracing() -> None:
    """Configura OpenTelemetry com exporter OTLP para Jaeger."""
    resource = Resource.create({"service.name": OTEL_SERVICE_NAME})
    provider = TracerProvider(resource=resource)
    exporter = OTLPSpanExporter(endpoint=OTEL_EXPORTER_OTLP_ENDPOINT, insecure=True)
    provider.add_span_processor(BatchSpanProcessor(exporter))
    trace.set_tracer_provider(provider)


def run_plan(order: dict) -> None:
    """Executa o plano completo para um pedido."""
    plan_id = f"plan_{uuid.uuid4().hex[:12]}"
    order_id = order.get("order_id", str(uuid.uuid4()))

    initial_state = {
        "order_id": order_id,
        "plan_id": plan_id,
        "user_id": order["user_id"],
        "items": order["items"],
        "total_amount": order["total_amount"],
        "currency": order.get("currency", "BRL"),
        "current_seq": 0,
        "events": [],
        "status": "planning",
    }

    logger.info("Iniciando plano %s para order %s", plan_id, order_id)

    graph = build_graph()
    result = graph.invoke(initial_state)

    events = result["events"]
    status = result["status"]

    if not events:
        logger.warning("Nenhum evento gerado para plano %s", plan_id)
        return

    logger.info(
        "Plano %s: %d eventos gerados (status=%s)", plan_id, len(events), status
    )

    # Publicar no Kafka
    producer = create_producer()
    publish_plan(producer, events)

    logger.info("Plano %s finalizado", plan_id)


def main() -> None:
    parser = argparse.ArgumentParser(description="Nexus Agent Planner")
    parser.add_argument(
        "--order",
        type=str,
        help="JSON do pedido (inline ou @arquivo.json)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Gera eventos mas nao publica no Kafka",
    )
    args = parser.parse_args()

    setup_tracing()

    if args.order:
        if args.order.startswith("@"):
            with open(args.order[1:]) as f:
                order = json.load(f)
        else:
            order = json.loads(args.order)
    else:
        # Pedido exemplo para dev/teste
        order = {
            "user_id": "usr_demo_001",
            "items": [
                {"product_id": "prod_abc", "quantity": 2, "unit_price": 49.90},
                {"product_id": "prod_xyz", "quantity": 1, "unit_price": 149.90},
            ],
            "total_amount": 249.70,
            "currency": "BRL",
        }
        logger.info("Usando pedido de exemplo (nenhum --order fornecido)")

    if args.dry_run:
        plan_id = f"plan_{uuid.uuid4().hex[:12]}"
        order_id = order.get("order_id", str(uuid.uuid4()))
        initial_state = {
            "order_id": order_id,
            "plan_id": plan_id,
            "user_id": order["user_id"],
            "items": order["items"],
            "total_amount": order["total_amount"],
            "currency": order.get("currency", "BRL"),
            "current_seq": 0,
            "events": [],
            "status": "planning",
        }
        graph = build_graph()
        result = graph.invoke(initial_state)
        print(json.dumps(result["events"], indent=2, ensure_ascii=False))
        return

    run_plan(order)


if __name__ == "__main__":
    main()
