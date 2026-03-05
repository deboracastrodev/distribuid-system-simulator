FROM python:3.12-slim AS base

WORKDIR /app

COPY pyproject.toml .
RUN pip install --no-cache-dir ".[dev]"

COPY src/ src/
COPY tests/ tests/

CMD ["python", "-m", "src.main"]
