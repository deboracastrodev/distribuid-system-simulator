"""Entrypoint do Nexus Agent Planner.

Gera planos de pedido e publica os eventos no Kafka, passo a passo. Com as
flags de simulação, estoque e pagamento falham em uma fração dos planos, que
terminam em ABORT_PLAN; com --orders, vira um gerador de carga.
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
import uuid
from collections import Counter
from functools import partial

from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter

from src.config import (
    OTEL_EXPORTER_OTLP_ENDPOINT,
    OTEL_EXPORTER_OTLP_INSECURE,
    OTEL_SERVICE_NAME,
    SIM_INVENTORY_FAILURE_RATE,
    SIM_PAYMENT_REJECTION_RATE,
    SIM_STEP_DELAY_MS,
    validate_config,
)
from src.planner.graph import build_graph
from src.planner.simulation import Simulation
from src.producer.kafka_producer import (
    PublishError,
    check_kafka_connectivity,
    create_producer,
    publish_event,
)
from src.runner import PlanOutcome, run_plan

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
# No handler, não no logger raiz: filtros de logger não se aplicam a records
# que sobem de loggers filhos (src.*, opentelemetry.*).
for _handler in logging.getLogger().handlers:
    _handler.addFilter(_TraceIDFilter())
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


def _load_order(arg: str | None) -> dict:
    if not arg:
        logger.info("Usando pedido de exemplo (nenhum --order fornecido)")
        return _default_order()
    if arg.startswith("@"):
        with open(arg[1:]) as f:
            return json.load(f)
    return json.loads(arg)


def _orders(base: dict, count: int):
    """Um pedido por plano. O order_id de --order só vale para um pedido."""
    if count > 1 and "order_id" in base:
        logger.warning("--orders %d: ignorando order_id de --order; cada plano recebe um novo", count)
    for _ in range(count):
        order = dict(base)
        if count > 1:
            order["order_id"] = str(uuid.uuid4())
        yield order


def _summary(outcomes: list[PlanOutcome]) -> str:
    reasons = Counter(o.abort_reason for o in outcomes if o.outcome == "aborted")
    completed = sum(o.outcome == "completed" for o in outcomes)
    detail = ", ".join(f"{r}: {n}" for r, n in sorted(reasons.items()))
    aborted = sum(reasons.values())
    return f"{len(outcomes)} planos: {completed} completos, {aborted} abortados" + (f" ({detail})" if detail else "")


def _write_report(path: str, simulation: Simulation, outcomes: list[PlanOutcome]) -> None:
    report = {"simulation": simulation.as_state(), "plans": [o.as_report() for o in outcomes]}
    with open(path, "w") as f:
        json.dump(report, f, indent=2)
    logger.info("Relatório gravado em %s", path)


def main() -> None:
    parser = argparse.ArgumentParser(description="Nexus Agent Planner")
    parser.add_argument("--order", type=str, help="JSON do pedido (inline ou @arquivo.json)")
    parser.add_argument("--orders", type=int, default=1, help="Quantos planos gerar (default: 1)")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Gera eventos sem publicar. Valida conectividade com Kafka.",
    )
    parser.add_argument(
        "--inventory-failure-rate", type=float, default=SIM_INVENTORY_FAILURE_RATE,
        help="Fração dos planos em que o estoque falha (0 a 1)",
    )
    parser.add_argument(
        "--payment-rejection-rate", type=float, default=SIM_PAYMENT_REJECTION_RATE,
        help="Fração dos planos em que o pagamento é recusado (0 a 1)",
    )
    parser.add_argument(
        "--step-delay-ms", type=int, default=SIM_STEP_DELAY_MS,
        help="Atraso entre eventos de um mesmo plano, em ms",
    )
    parser.add_argument(
        "--seed", type=str, default=None,
        help="Seed das falhas simuladas; a mesma seed reproduz a mesma execução",
    )
    parser.add_argument("--report", type=str, help="Grava o resultado de cada plano em JSON")
    args = parser.parse_args()

    if args.orders < 1:
        parser.error("--orders deve ser >= 1")
    if args.step_delay_ms < 0:
        parser.error("--step-delay-ms deve ser >= 0")
    try:
        simulation = Simulation(
            inventory_failure_rate=args.inventory_failure_rate,
            payment_rejection_rate=args.payment_rejection_rate,
            seed=args.seed if args.seed is not None else uuid.uuid4().hex[:12],
        )
    except ValueError as e:
        parser.error(str(e))

    validate_config()
    setup_tracing()
    logger.info("Simulação: %s", simulation)

    graph = build_graph()
    base_order = _load_order(args.order)

    if args.dry_run:
        if not check_kafka_connectivity():
            logger.error("dry-run: Kafka inacessivel — verifique se os containers estao rodando")
        events: list[dict] = []
        outcomes = [
            run_plan(graph, order, simulation.for_plan(i), events.append)
            for i, order in enumerate(_orders(base_order, args.orders))
        ]
        print(json.dumps(events, indent=2, ensure_ascii=False))
        logger.info("dry-run: %s; eventos nao publicados", _summary(outcomes))
        return

    publish = partial(publish_event, create_producer())
    outcomes: list[PlanOutcome] = []
    try:
        for i, order in enumerate(_orders(base_order, args.orders)):
            outcomes.append(run_plan(graph, order, simulation.for_plan(i), publish, args.step_delay_ms / 1000))
    except PublishError as e:
        logger.error("Publicação falhou no plano %d: %s", len(outcomes) + 1, e)
        if args.report:
            _write_report(args.report, simulation, outcomes)
        sys.exit(1)

    logger.info(_summary(outcomes))
    if args.report:
        _write_report(args.report, simulation, outcomes)


if __name__ == "__main__":
    main()
