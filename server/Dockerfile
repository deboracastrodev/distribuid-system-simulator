FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy all source first, then resolve dependencies
COPY . .
RUN go mod tidy && go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /nexus-server ./cmd/server

# --- Runtime ---
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /nexus-server /usr/local/bin/nexus-server
COPY scripts/lua/ /app/scripts/lua/

EXPOSE 8080

ENTRYPOINT ["nexus-server"]
