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

from src.config import (
    OTEL_EXPORTER_OTLP_ENDPOINT,
    OTEL_EXPORTER_OTLP_INSECURE,
    OTEL_SERVICE_NAME,
    validate_config,
)
from src.models.events import generate_plan_id
from src.planner.graph import build_graph
from src.producer.kafka_producer import check_kafka_connectivity, create_producer, publish_plan

class _TraceIDFilter(logging.Filter):
    """Injeta trace_id do OpenTelemetry nos log records para correlação."""
    def filter(self, record):
        span = trace.get_current_span()
        ctx = span.get_span_context()
        record.otelTraceID = format(ctx.trace_id, "032x") if ctx.trace_id else "0" * 32
        return True

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s [trace_id=%(otelTraceID)s]",
)
logging.getLogger().addFilter(_TraceIDFilter())
logger = logging.getLogger(__name__)


def setup_tracing() -> None:
    """Configura OpenTelemetry com exporter OTLP para Jaeger."""
    resource = Resource.create({"service.name": OTEL_SERVICE_NAME})
    provider = TracerProvider(resource=resource)
    exporter = OTLPSpanExporter(
        endpoint=OTEL_EXPORTER_OTLP_ENDPOINT,
        insecure=OTEL_EXPORTER_OTLP_INSECURE,
    )
    provider.add_span_processor(BatchSpanProcessor(exporter))
    trace.set_tracer_provider(provider)


def _build_initial_state(order: dict) -> dict:
    """Constroi estado inicial do plano a partir de um pedido."""
    return {
        "order_id": order.get("order_id", str(uuid.uuid4())),
        "plan_id": generate_plan_id(),
        "user_id": order["user_id"],
        "items": order["items"],
        "total_amount": order["total_amount"],
        "currency": order.get("currency", "BRL"),
        "current_seq": 0,
        "events": [],
        "status": "planning",
        "abort_reason": "",
    }


def run_plan(order: dict) -> None:
    """Executa o plano completo para um pedido."""
    tracer = trace.get_tracer(__name__)

    with tracer.start_as_current_span("run-plan") as span:
        state = _build_initial_state(order)
        span.set_attribute("plan.id", state["plan_id"])
        span.set_attribute("order.id", state["order_id"])

        logger.info("Iniciando plano %s para order %s", state["plan_id"], state["order_id"])

        graph = build_graph()
        result = graph.invoke(state)

        events = result["events"]
        status = result["status"]

        if not events:
            logger.warning("Nenhum evento gerado para plano %s", state["plan_id"])
            return

        logger.info(
            "Plano %s: %d eventos gerados (status=%s)", state["plan_id"], len(events), status
        )

        producer = create_producer()
        publish_plan(producer, events)

        logger.info("Plano %s finalizado", state["plan_id"])


def _default_order() -> dict:
    return {
        "user_id": "usr_demo_001",
        "items": [
            {"product_id": "prod_abc", "quantity": 2, "unit_price": 49.90},
            {"product_id": "prod_xyz", "quantity": 1, "unit_price": 149.90},
        ],
        "total_amount": 249.70,
        "currency": "BRL",
    }


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
        help="Gera eventos sem publicar. Valida conectividade com Kafka.",
    )
    args = parser.parse_args()

    validate_config()
    setup_tracing()

    if args.order:
        if args.order.startswith("@"):
            with open(args.order[1:]) as f:
                order = json.load(f)
        else:
            order = json.loads(args.order)
    else:
        order = _default_order()
        logger.info("Usando pedido de exemplo (nenhum --order fornecido)")

    if args.dry_run:
        # Validar conectividade com dependencias
        kafka_ok = check_kafka_connectivity()
        if not kafka_ok:
            logger.error("dry-run: Kafka inacessivel — verifique se os containers estao rodando")

        state = _build_initial_state(order)
        graph = build_graph()
        result = graph.invoke(state)
        print(json.dumps(result["events"], indent=2, ensure_ascii=False))

        if kafka_ok:
            logger.info("dry-run: Kafka OK, eventos nao publicados")
        return

    run_plan(order)


if __name__ == "__main__":
    main()
