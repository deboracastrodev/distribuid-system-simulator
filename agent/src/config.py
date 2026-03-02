import os
import sys

from dotenv import load_dotenv

load_dotenv()

KAFKA_BOOTSTRAP_SERVERS = os.getenv("KAFKA_BOOTSTRAP_SERVERS", "localhost:9092")
KAFKA_TOPIC = os.getenv("KAFKA_TOPIC", "orders")

OTEL_EXPORTER_OTLP_ENDPOINT = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
OTEL_EXPORTER_OTLP_INSECURE = os.getenv("OTEL_EXPORTER_OTLP_INSECURE", "true").lower() == "true"
OTEL_SERVICE_NAME = os.getenv("OTEL_SERVICE_NAME", "nexus-agent")

# Limite de valor maximo por pedido (regra de negocio)
ORDER_MAX_AMOUNT = float(os.getenv("ORDER_MAX_AMOUNT", "100000.00"))


def validate_config() -> None:
    """Valida variaveis criticas no startup. Falha fast se invalido."""
    errors = []

    if not KAFKA_BOOTSTRAP_SERVERS or KAFKA_BOOTSTRAP_SERVERS.isspace():
        errors.append("KAFKA_BOOTSTRAP_SERVERS nao definido ou vazio")

    if not KAFKA_TOPIC or KAFKA_TOPIC.isspace():
        errors.append("KAFKA_TOPIC nao definido ou vazio")

    if not OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_ENDPOINT.isspace():
        errors.append("OTEL_EXPORTER_OTLP_ENDPOINT nao definido ou vazio")

    if errors:
        for e in errors:
            print(f"[CONFIG ERROR] {e}", file=sys.stderr)
        sys.exit(1)
