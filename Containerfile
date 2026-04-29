# Build stage
FROM registry.access.redhat.com/ubi9/go-toolset:1.25.5 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

USER root
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o three-tier-app-demo-service-provider ./cmd/three-tier-app-demo-service-provider

# Runtime stage
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

WORKDIR /app

COPY --from=builder /app/three-tier-app-demo-service-provider .

EXPOSE 8080

ENTRYPOINT ["./three-tier-app-demo-service-provider"]
