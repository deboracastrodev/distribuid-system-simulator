"""O grafo compilado de ponta a ponta: roteamento para abort_plan nas falhas simuladas."""

import pytest

from src.planner.graph import build_graph
from src.planner.simulation import Simulation
from src.runner import build_initial_state

ORDER = {
    "user_id": "user_123",
    "items": [{"product_id": "item_1", "quantity": 2, "unit_price": 50.0}],
    "total_amount": 100.0,
}

HAPPY_PATH = ["OrderCreated", "InventoryValidated", "PaymentProcessed", "OrderShipped", "OrderCompleted"]


@pytest.fixture(scope="module")
def graph():
    return build_graph()


def run(graph, **simulation):
    return graph.invoke(build_initial_state(ORDER, Simulation(seed="t", **simulation)))


def types(state):
    return [e["event_type"] for e in state["events"]]


def test_no_failures_follows_happy_path(graph):
    final = run(graph)
    assert types(final) == HAPPY_PATH
    assert [e["seq_id"] for e in final["events"]] == [1, 2, 3, 4, 5]
    assert final["status"] == "publishing"


def test_inventory_failure_aborts_after_order_created(graph):
    final = run(graph, inventory_failure_rate=1.0)
    assert types(final) == ["OrderCreated", "ABORT_PLAN"]
    abort = final["events"][-1]
    assert abort["data"]["abort_code"] == "inventory_failed"
    assert abort["data"]["aborted_at_seq"] == 1
    assert "seq_id" not in abort
    assert final["status"] == "aborted"
    assert final["abort_reason"] == "inventory_failed"


def test_payment_rejection_aborts_without_payment_processed(graph):
    final = run(graph, payment_rejection_rate=1.0)
    assert types(final) == ["OrderCreated", "InventoryValidated", "ABORT_PLAN"]
    abort = final["events"][-1]
    assert abort["data"]["abort_code"] == "payment_rejected"
    assert abort["data"]["aborted_at_seq"] == 2


def test_inventory_failure_wins_over_payment_rejection(graph):
    final = run(graph, inventory_failure_rate=1.0, payment_rejection_rate=1.0)
    assert types(final) == ["OrderCreated", "ABORT_PLAN"]


def test_business_rule_abort_emits_only_abort_plan(graph):
    final = graph.invoke(build_initial_state({**ORDER, "items": []}, Simulation()))
    assert types(final) == ["ABORT_PLAN"]
    assert final["events"][0]["data"]["aborted_at_seq"] == 0


def test_state_without_simulation_follows_happy_path(graph):
    """O LangGraph Studio pode mandar um state sem o campo simulation."""
    state = build_initial_state(ORDER, Simulation())
    del state["simulation"]
    assert types(graph.invoke(state)) == HAPPY_PATH


def test_same_seed_reproduces_a_run_despite_new_ids(graph):
    """order_id e plan_id mudam a cada execução; as decisões não."""
    run = Simulation(inventory_failure_rate=0.4, payment_rejection_rate=0.4, seed="repro")

    def outcomes(sim):
        return [types(graph.invoke(build_initial_state(ORDER, sim.for_plan(i)))) for i in range(20)]

    first = outcomes(run)
    assert outcomes(run) == first
    assert len({tuple(t) for t in first}) == 3  # completo, falha de estoque, pagamento recusado
    assert outcomes(Simulation(inventory_failure_rate=0.4, payment_rejection_rate=0.4, seed="other")) != first
