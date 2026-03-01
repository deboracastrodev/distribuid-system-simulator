"""Grafo LangGraph do Agent Planner.

Fluxo:
  START -> generate_plan -> [abort | create_order_event]
  create_order_event -> create_inventory_event -> create_payment_event
  -> create_shipping_event -> create_completion_event -> END
"""

from __future__ import annotations

from langgraph.graph import END, StateGraph

from src.planner.nodes import (
    abort_plan,
    create_completion_event,
    create_inventory_event,
    create_order_event,
    create_payment_event,
    create_shipping_event,
    generate_plan,
)
from src.planner.state import PlanState


def _route_after_plan(state: PlanState) -> str:
    """Redireciona para abort se validacao falhou."""
    if state["status"] == "aborted":
        return "abort_plan"
    return "create_order_event"


def build_graph() -> StateGraph:
    """Constroi e compila o grafo do planner."""
    graph = StateGraph(PlanState)

    # Nodes
    graph.add_node("generate_plan", generate_plan)
    graph.add_node("create_order_event", create_order_event)
    graph.add_node("create_inventory_event", create_inventory_event)
    graph.add_node("create_payment_event", create_payment_event)
    graph.add_node("create_shipping_event", create_shipping_event)
    graph.add_node("create_completion_event", create_completion_event)
    graph.add_node("abort_plan", abort_plan)

    # Edges
    graph.set_entry_point("generate_plan")
    graph.add_conditional_edges("generate_plan", _route_after_plan)
    graph.add_edge("create_order_event", "create_inventory_event")
    graph.add_edge("create_inventory_event", "create_payment_event")
    graph.add_edge("create_payment_event", "create_shipping_event")
    graph.add_edge("create_shipping_event", "create_completion_event")
    graph.add_edge("create_completion_event", END)
    graph.add_edge("abort_plan", END)

    return graph.compile()
