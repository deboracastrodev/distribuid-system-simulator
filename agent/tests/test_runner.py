import pytest

from src.planner.graph import build_graph
from src.planner.simulation import Simulation
from src.producer.kafka_producer import PublishError
from src.runner import run_plan

ORDER = {
    "order_id": "8c7d3f0e-0000-4000-8000-000000000001",
    "user_id": "user_123",
    "items": [{"product_id": "item_1", "quantity": 1, "unit_price": 10.0}],
    "total_amount": 10.0,
}


@pytest.fixture(scope="module")
def graph():
    return build_graph()


def test_completed_plan_publishes_every_event_in_order(graph):
    published = []
    outcome = run_plan(graph, ORDER, Simulation(), published.append)

    assert [e["seq_id"] for e in published] == [1, 2, 3, 4, 5]
    assert {e["order_id"] for e in published} == {ORDER["order_id"]}
    assert len({e["plan_id"] for e in published}) == 1
    assert outcome.outcome == "completed"
    assert outcome.abort_reason == ""
    assert outcome.last_seq == 5
    assert outcome.events == [e["event_type"] for e in published]
    assert outcome.plan_id == published[0]["plan_id"]
    assert outcome.order_id == ORDER["order_id"]


def test_aborted_plan_reports_reason_and_last_sequenced_event(graph):
    published = []
    outcome = run_plan(graph, ORDER, Simulation(payment_rejection_rate=1.0, seed="s:7"), published.append)

    assert outcome.outcome == "aborted"
    assert outcome.abort_reason == "payment_rejected"
    assert outcome.events == ["OrderCreated", "InventoryValidated", "ABORT_PLAN"]
    assert outcome.last_seq == 2
    assert outcome.seed == "s:7"
    assert outcome.as_report()["last_seq"] == 2


def test_events_are_published_as_nodes_run_not_at_the_end(graph):
    """Cada evento sai antes de o node seguinte executar."""
    timeline = []

    class Spy:
        def stream(self, state, stream_mode):
            for values in graph.stream(state, stream_mode=stream_mode):
                timeline.append(("step", len(values["events"])))
                yield values

    run_plan(Spy(), ORDER, Simulation(), lambda e: timeline.append(("publish", e["seq_id"])))

    # stream(values) emite o state inicial, o de generate_plan (sem eventos) e um
    # por node; cada evento é publicado antes do passo seguinte.
    assert timeline == [
        ("step", 0), ("step", 0),
        ("step", 1), ("publish", 1),
        ("step", 2), ("publish", 2),
        ("step", 3), ("publish", 3),
        ("step", 4), ("publish", 4),
        ("step", 5), ("publish", 5),
    ]

def test_step_delay_sleeps_between_events_only(graph):
    sleeps = []
    run_plan(graph, ORDER, Simulation(), lambda e: None, step_delay=0.25, sleep=sleeps.append)
    assert sleeps == [0.25] * 4


def test_no_delay_never_sleeps(graph):
    sleeps = []
    run_plan(graph, ORDER, Simulation(), lambda e: None, sleep=sleeps.append)
    assert sleeps == []


def test_publish_error_stops_the_plan(graph):
    published = []

    def publish(event):
        if event["seq_id"] == 3:
            raise PublishError("broker down")
        published.append(event)

    with pytest.raises(PublishError):
        run_plan(graph, ORDER, Simulation(), publish)
    assert [e["seq_id"] for e in published] == [1, 2]


def test_each_run_gets_a_new_plan_id(graph):
    a = run_plan(graph, ORDER, Simulation(), lambda e: None)
    b = run_plan(graph, ORDER, Simulation(), lambda e: None)
    assert a.plan_id != b.plan_id
