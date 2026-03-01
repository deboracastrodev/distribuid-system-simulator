"""Producer Kafka com idempotencia, ordering por order_id e tracing W3C."""

from __future__ import annotations

import json
import logging

from confluent_kafka import Producer
from opentelemetry import trace

from src.config import KAFKA_BOOTSTRAP_SERVERS, KAFKA_TOPIC

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)


def _delivery_callback(err, msg):
    if err:
        logger.error("Falha na entrega: %s", err)
    else:
        logger.info(
            "Evento entregue: topic=%s partition=%s offset=%s key=%s",
            msg.topic(),
            msg.partition(),
            msg.offset(),
            msg.key().decode() if msg.key() else None,
        )


def create_producer() -> Producer:
    return Producer(
        {
            "bootstrap.servers": KAFKA_BOOTSTRAP_SERVERS,
            "acks": "all",
            "enable.idempotence": True,
            "retries": 5,
            "max.in.flight.requests.per.connection": 1,
        }
    )


def _build_traceparent() -> str:
    """Gera header traceparent W3C via OpenTelemetry SDK."""
    span = trace.get_current_span()
    ctx = span.get_span_context()
    if ctx.is_valid:
        return f"00-{format(ctx.trace_id, '032x')}-{format(ctx.span_id, '016x')}-01"
    # Fallback: criar span efemero para gerar IDs
    with tracer.start_as_current_span("generate-traceparent") as s:
        c = s.get_span_context()
        return f"00-{format(c.trace_id, '032x')}-{format(c.span_id, '016x')}-01"


def publish_event(producer: Producer, event: dict) -> None:
    """Publica um unico evento no Kafka com ordering key e traceparent."""
    traceparent = _build_traceparent()
    producer.produce(
        topic=KAFKA_TOPIC,
        key=event["order_id"],
        value=json.dumps(event).encode(),
        headers=[("traceparent", traceparent.encode())],
        callback=_delivery_callback,
    )
    producer.poll(0)


def publish_plan(producer: Producer, events: list[dict]) -> None:
    """Publica todos os eventos de um plano em sequencia e faz flush."""
    with tracer.start_as_current_span("publish-plan") as span:
        plan_id = events[0]["plan_id"] if events else "unknown"
        span.set_attribute("plan.id", plan_id)
        span.set_attribute("plan.event_count", len(events))

        for event in events:
            publish_event(producer, event)

        remaining = producer.flush(timeout=10)
        if remaining > 0:
            logger.warning("%d mensagens nao entregues apos flush", remaining)
        else:
            logger.info("Plano %s: %d eventos entregues com sucesso", plan_id, len(events))
