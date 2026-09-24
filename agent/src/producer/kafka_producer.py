"""Producer Kafka com idempotencia, ordering por order_id e tracing W3C."""

from __future__ import annotations

import json
import logging

from confluent_kafka import KafkaException, Producer
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


class PublishError(Exception):
    """O broker não confirmou a entrega de um evento."""


def publish_event(producer: Producer, event: dict, timeout: float = 10.0) -> None:
    """Publica um evento e espera a confirmação do broker.

    Levanta PublishError se a entrega falhar ou não for confirmada no prazo:
    um plano publicado pela metade deixaria o server esperando um evento que
    nunca chega, então quem chama precisa saber.
    """
    with tracer.start_as_current_span(
        f"publish-event-{event.get('event_type', 'unknown')}"
    ) as span:
        span.set_attribute("event.type", event.get("event_type", ""))
        span.set_attribute("event.plan_id", event.get("plan_id", ""))
        span.set_attribute("event.seq_id", event.get("seq_id", 0))

        errors: list = []

        def on_delivery(err, msg):
            if err:
                errors.append(err)
            else:
                _delivery_callback(err, msg)

        producer.produce(
            topic=KAFKA_TOPIC,
            key=event["order_id"],
            value=json.dumps(event).encode(),
            headers=[("traceparent", _build_traceparent(span).encode())],
            callback=on_delivery,
        )
        remaining = producer.flush(timeout=timeout)
        if errors:
            raise PublishError(f"entrega de {event['event_type']} falhou: {errors[0]}")
        if remaining:
            raise PublishError(f"entrega de {event['event_type']} não confirmada em {timeout}s")
