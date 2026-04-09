FROM registry.access.redhat.com/ubi9/go-toolset:1.25.5 AS builder

WORKDIR /build

COPY go.mod go.sum ./
USER root
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o 3-tier-demo-service-provider ./cmd/3-tier-demo-service-provider

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

WORKDIR /app

COPY --from=builder /build/3-tier-demo-service-provider .

EXPOSE 8080

ENTRYPOINT ["./3-tier-demo-service-provider"]
