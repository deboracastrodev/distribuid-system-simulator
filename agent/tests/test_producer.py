import json

import pytest

from src.config import KAFKA_TOPIC
from src.producer.kafka_producer import PublishError, publish_event

EVENT = {
    "event_type": "OrderCreated",
    "plan_id": "plan_0123456789abcdef",
    "seq_id": 1,
    "order_id": "8c7d3f0e-0000-4000-8000-000000000001",
}


class FakeMessage:
    def topic(self):
        return KAFKA_TOPIC

    def partition(self):
        return 0

    def offset(self):
        return 42

    def key(self):
        return EVENT["order_id"].encode()


class FakeProducer:
    def __init__(self, error=None, remaining=0):
        self.error = error
        self.remaining = remaining
        self.produced = []
        self._pending = []

    def produce(self, topic, key, value, headers, callback):
        self.produced.append({"topic": topic, "key": key, "value": value, "headers": headers})
        self._pending.append(callback)

    def flush(self, timeout):
        if not self.remaining:
            for callback in self._pending:
                callback(self.error, FakeMessage())
        self._pending.clear()
        return self.remaining


def test_publishes_keyed_by_order_with_traceparent():
    producer = FakeProducer()
    publish_event(producer, EVENT)

    [msg] = producer.produced
    assert msg["topic"] == KAFKA_TOPIC
    assert msg["key"] == EVENT["order_id"]
    assert json.loads(msg["value"]) == EVENT
    [(name, value)] = msg["headers"]
    assert name == "traceparent"
    assert value.decode().startswith("00-")


def test_delivery_error_raises():
    with pytest.raises(PublishError, match="OrderCreated"):
        publish_event(FakeProducer(error="broker down"), EVENT)


def test_unconfirmed_delivery_raises():
    with pytest.raises(PublishError, match="não confirmada"):
        publish_event(FakeProducer(remaining=1), EVENT, timeout=0.1)
