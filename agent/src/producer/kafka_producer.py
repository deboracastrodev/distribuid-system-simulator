"""Producer Kafka com idempotencia, ordering por order_id e tracing W3C."""

from __future__ import annotations

import json
import logging

from confluent_kafka import KafkaException, Producer
from opentelemetry import trace
from opentelemetry.context import get_current

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


def check_kafka_connectivity() -> bool:
    """Verifica conectividade basica com o Kafka. Retorna True se OK."""
    try:
        producer = create_producer()
        metadata = producer.list_topics(timeout=5)
        logger.info("Kafka conectado: %d brokers, %d topics",
                     len(metadata.brokers), len(metadata.topics))
        return True
    except KafkaException as e:
        logger.error("Kafka inacessivel: %s", e)
        return False


def _build_traceparent(span: trace.Span) -> str:
    """Extrai traceparent W3C do span ativo (sem criar spans efemeros)."""
    ctx = span.get_span_context()
    return f"00-{format(ctx.trace_id, '032x')}-{format(ctx.span_id, '016x')}-01"


def publish_plan(producer: Producer, events: list[dict]) -> None:
    """Publica todos os eventos de um plano em sequencia e faz flush."""
    with tracer.start_as_current_span("publish-plan") as plan_span:
        plan_id = events[0]["plan_id"] if events else "unknown"
        plan_span.set_attribute("plan.id", plan_id)
        plan_span.set_attribute("plan.event_count", len(events))

        for event in events:
            with tracer.start_as_current_span(
                f"publish-event-{event.get('event_type', 'unknown')}"
            ) as event_span:
                event_span.set_attribute("event.type", event.get("event_type", ""))
                event_span.set_attribute("event.seq_id", event.get("seq_id", 0))

                traceparent = _build_traceparent(event_span)
                producer.produce(
                    topic=KAFKA_TOPIC,
                    key=event["order_id"],
                    value=json.dumps(event).encode(),
                    headers=[("traceparent", traceparent.encode())],
                    callback=_delivery_callback,
                )

        # Processar callbacks pendentes de uma vez (nao dentro do loop)
        producer.poll(0)

        remaining = producer.flush(timeout=10)
        if remaining > 0:
            logger.warning("%d mensagens nao entregues apos flush", remaining)
        else:
            logger.info("Plano %s: %d eventos entregues com sucesso", plan_id, len(events))
