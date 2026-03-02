module github.com/user/nexus-server

go 1.22

require (
	github.com/hashicorp/consul/api v1.29.1
	github.com/jackc/pgx/v5 v5.6.0
	github.com/redis/go-redis/v9 v9.5.3
	github.com/twmb/franz-go v1.17.0
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.27.0
	go.opentelemetry.io/otel/sdk v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
)

require (
	google.golang.org/genproto/googleapis/api v0.0.0-20240520151616-dc85e6b867a5
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240520151616-dc85e6b867a5
)

exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1
