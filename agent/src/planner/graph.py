"""Grafo LangGraph do Agent Planner.

Fluxo:
  START -> generate_plan -> [abort_plan | create_order_event]
  create_order_event -> create_inventory_event -> [abort_plan | create_payment_event]
  create_payment_event -> [abort_plan | create_shipping_event]
  create_shipping_event -> create_completion_event -> END

Estoque e pagamento podem falhar conforme a simulação no state
(src/planner/simulation.py); a falha leva ao abort_plan com o código do passo.
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


def _abort_or(next_node: str):
    """Roteador: abort_plan se o node anterior abortou, senão next_node."""
    def route(state: PlanState) -> str:
        return "abort_plan" if state["status"] == "aborted" else next_node
    return route


def build_graph():
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

    # Edges (paths explicitos para visualizacao no LangGraph Studio)
    graph.set_entry_point("generate_plan")
    for node, next_node in (
        ("generate_plan", "create_order_event"),
        ("create_inventory_event", "create_payment_event"),
        ("create_payment_event", "create_shipping_event"),
    ):
        graph.add_conditional_edges(
            node,
            _abort_or(next_node),
            {"abort_plan": "abort_plan", next_node: next_node},
        )
    graph.add_edge("create_order_event", "create_inventory_event")
    graph.add_edge("create_shipping_event", "create_completion_event")
    graph.add_edge("create_completion_event", END)
    graph.add_edge("abort_plan", END)

    return graph.compile()


# Grafo compilado exposto como variavel para o LangGraph Studio
graph = build_graph()
