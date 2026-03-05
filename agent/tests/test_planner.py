import uuid

import pytest

from src.planner.nodes import (
    abort_plan,
    create_completion_event,
    create_inventory_event,
    create_order_event,
    create_payment_event,
    create_shipping_event,
    generate_plan,
)


@pytest.fixture
def base_state():
    return {
        "plan_id": str(uuid.uuid4()),
        "order_id": str(uuid.uuid4()),
        "user_id": "user_123",
        "items": [{"product_id": "item_1", "quantity": 2, "unit_price": 50.0}],
        "total_amount": 100.0,
        "currency": "BRL",
        "current_seq": 0,
        "events": [],
        "status": "idle",
        "abort_reason": "",
    }


# --- generate_plan ---

def test_generate_plan_ok(base_state):
    result = generate_plan(base_state)
    assert result["status"] == "planning"
    assert result["current_seq"] == 0
    assert result["events"] == []


def test_generate_plan_empty_items(base_state):
    base_state["items"] = []
    result = generate_plan(base_state)
    assert result["status"] == "aborted"
    assert result["abort_reason"] == "items_empty"


def test_generate_plan_amount_mismatch(base_state):
    base_state["total_amount"] = 999.0
    result = generate_plan(base_state)
    assert result["status"] == "aborted"
    assert result["abort_reason"] == "amount_mismatch"


def test_generate_plan_invalid_amount(base_state):
    base_state["total_amount"] = 0
    result = generate_plan(base_state)
    assert result["status"] == "aborted"
    assert result["abort_reason"] == "invalid_amount"


def test_generate_plan_exceeds_limit(base_state):
    base_state["items"] = [{"product_id": "x", "quantity": 1, "unit_price": 200000.0}]
    base_state["total_amount"] = 200000.0
    result = generate_plan(base_state)
    assert result["status"] == "aborted"
    assert result["abort_reason"] == "amount_exceeds_limit"


# --- Event creation nodes ---

def test_create_order_event(base_state):
    base_state["status"] = "planning"
    result = create_order_event(base_state)
    assert result["current_seq"] == 1
    assert len(result["events"]) == 1
    assert result["events"][0]["event_type"] == "OrderCreated"
    assert result["events"][0]["seq_id"] == 1


def test_create_inventory_event(base_state):
    base_state["current_seq"] = 1
    base_state["events"] = [{"event_type": "OrderCreated", "seq_id": 1}]
    result = create_inventory_event(base_state)
    assert result["current_seq"] == 2
    assert len(result["events"]) == 2
    assert result["events"][-1]["event_type"] == "InventoryValidated"


def test_create_payment_event(base_state):
    base_state["current_seq"] = 2
    base_state["events"] = [{"seq_id": 1}, {"seq_id": 2}]
    result = create_payment_event(base_state)
    assert result["current_seq"] == 3
    assert result["events"][-1]["event_type"] == "PaymentProcessed"


def test_create_shipping_event(base_state):
    base_state["current_seq"] = 3
    base_state["events"] = [{"seq_id": 1}, {"seq_id": 2}, {"seq_id": 3}]
    result = create_shipping_event(base_state)
    assert result["current_seq"] == 4
    assert result["events"][-1]["event_type"] == "OrderShipped"


def test_create_completion_event(base_state):
    base_state["current_seq"] = 4
    base_state["events"] = [{"seq_id": i} for i in range(1, 5)]
    result = create_completion_event(base_state)
    assert result["current_seq"] == 5
    assert result["events"][-1]["event_type"] == "OrderCompleted"
    assert result["status"] == "publishing"


# --- Full flow ---

def test_full_successful_flow(base_state):
    state = {**base_state, **generate_plan(base_state)}
    state = {**state, **create_order_event(state)}
    state = {**state, **create_inventory_event(state)}
    state = {**state, **create_payment_event(state)}
    state = {**state, **create_shipping_event(state)}
    state = {**state, **create_completion_event(state)}

    assert state["current_seq"] == 5
    assert len(state["events"]) == 5
    assert state["status"] == "publishing"

    types = [e["event_type"] for e in state["events"]]
    assert types == [
        "OrderCreated",
        "InventoryValidated",
        "PaymentProcessed",
        "OrderShipped",
        "OrderCompleted",
    ]

    seqs = [e["seq_id"] for e in state["events"]]
    assert seqs == [1, 2, 3, 4, 5]

    # All events must have unique event_ids
    event_ids = [e["event_id"] for e in state["events"]]
    assert len(set(event_ids)) == 5


def test_events_have_required_fields(base_state):
    state = {**base_state, **generate_plan(base_state)}
    state = {**state, **create_order_event(state)}

    event = state["events"][0]
    assert "event_id" in event
    assert "event_type" in event
    assert "plan_id" in event
    assert "seq_id" in event
    assert "order_id" in event
    assert "timestamp" in event
    assert "schema_version" in event
    assert "data" in event
    assert event["plan_id"] == base_state["plan_id"]
    assert event["order_id"] == base_state["order_id"]


# --- Abort ---

def test_abort_plan(base_state):
    base_state["abort_reason"] = "payment_rejected"
    base_state["current_seq"] = 3
    result = abort_plan(base_state)
    assert result["status"] == "aborted"
    assert len(result["events"]) == 1
    assert result["events"][0]["event_type"] == "ABORT_PLAN"
    assert "seq_id" not in result["events"][0]


def test_abort_plan_contains_reason_in_data(base_state):
    base_state["abort_reason"] = "inventory_failed"
    base_state["current_seq"] = 2
    result = abort_plan(base_state)
    abort_event = result["events"][0]
    assert "reason" in abort_event["data"]
    assert "abort_code" in abort_event["data"]
    assert abort_event["data"]["abort_code"] == "inventory_failed"
