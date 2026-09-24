#!/usr/bin/env python3
"""Receptor de webhooks para testes do dispatcher do outbox.

Registra cada entrega e verifica, por pedido (X-Aggregate-ID):
- duplicatas: a mesma Idempotency-Key aceita mais de uma vez;
- ordem: o seq_id do payload tem de crescer a cada entrega aceita;
- entregas sem Idempotency-Key.

Falhas injetáveis via POST /control:
- fail_every: N  -> a cada N requisições (contador global), responde 503;
- reject_aggregates: [order_id, ...] -> responde 400 para esses pedidos.

Endpoints:
    POST /webhook   recebe a notificação
    GET  /stats     contadores por pedido (JSON)
    POST /control   define o modo de falha (JSON)
    POST /reset     zera contadores e modo de falha
    GET  /health    200 OK

Só usa a biblioteca padrão: roda em python:3.12-slim sem build.
"""

from __future__ import annotations

import json
import os
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.getenv("WEBHOOK_SINK_PORT", "9090"))


class State:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.reset()

    def reset(self) -> None:
        self.requests = 0
        self.fail_every = 0
        self.reject_aggregates: set[str] = set()
        self.aggregates: dict[str, dict] = {}

    def aggregate(self, aggregate_id: str) -> dict:
        return self.aggregates.setdefault(aggregate_id, {
            "received": 0,
            "accepted": 0,
            "duplicates": 0,
            "out_of_order": 0,
            "missing_key": 0,
            "rejected": 0,
            "failed_injected": 0,
            "accepted_seqs": [],
            "_keys": set(),
            "_last_seq": 0,
        })

    def handle(self, key: str | None, aggregate_id: str, seq_id: int | None) -> int:
        """Decide o status da resposta e registra a entrega."""
        with self.lock:
            self.requests += 1
            agg = self.aggregate(aggregate_id)
            agg["received"] += 1

            if aggregate_id in self.reject_aggregates:
                agg["rejected"] += 1
                return 400
            if self.fail_every and self.requests % self.fail_every == 0:
                agg["failed_injected"] += 1
                return 503

            agg["accepted"] += 1
            if not key:
                agg["missing_key"] += 1
            elif key in agg["_keys"]:
                agg["duplicates"] += 1
            else:
                agg["_keys"].add(key)

            if seq_id is not None:
                if seq_id <= agg["_last_seq"]:
                    agg["out_of_order"] += 1
                agg["_last_seq"] = max(agg["_last_seq"], seq_id)
                agg["accepted_seqs"].append(seq_id)
            return 200

    def stats(self) -> dict:
        with self.lock:
            return {
                "requests": self.requests,
                "fail_every": self.fail_every,
                "reject_aggregates": sorted(self.reject_aggregates),
                "aggregates": {
                    agg_id: {k: v for k, v in agg.items() if not k.startswith("_")}
                    | {"accepted_unique": len(agg["_keys"])}
                    for agg_id, agg in self.aggregates.items()
                },
            }


STATE = State()


class Handler(BaseHTTPRequestHandler):
    def _json(self, status: int, body: dict) -> None:
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _body(self) -> bytes:
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def do_GET(self) -> None:
        if self.path == "/health":
            self._json(200, {"status": "ok"})
        elif self.path == "/stats":
            self._json(200, STATE.stats())
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self) -> None:
        body = self._body()
        if self.path == "/webhook":
            try:
                seq_id = json.loads(body).get("seq_id")
            except (ValueError, AttributeError):
                seq_id = None
            status = STATE.handle(
                self.headers.get("Idempotency-Key"),
                self.headers.get("X-Aggregate-ID", "unknown"),
                seq_id,
            )
            self._json(status, {"status": status})
        elif self.path == "/control":
            mode = json.loads(body or b"{}")
            with STATE.lock:
                STATE.fail_every = int(mode.get("fail_every", 0))
                STATE.reject_aggregates = set(mode.get("reject_aggregates", []))
            self._json(200, STATE.stats())
        elif self.path == "/reset":
            with STATE.lock:
                STATE.reset()
            self._json(200, {"status": "reset"})
        else:
            self._json(404, {"error": "not found"})

    def log_message(self, fmt: str, *args) -> None:  # silencia o log por requisição
        pass


if __name__ == "__main__":
    # Como PID 1 no container, o processo ignora SIGTERM sem um handler, e o
    # `docker compose down` esperaria o timeout de 10s para matá-lo.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    print(f"webhook sink ouvindo na porta {PORT}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
