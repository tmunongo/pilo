# syntax=docker/dockerfile:1
# Multi-stage Dockerfile for Pilo

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY internal/ ./internal/
COPY web/ ./web/

ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o pilo main.go

FROM alpine:3.20

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/pilo /app/pilo

VOLUME ["/data"]

EXPOSE 8080

ENV PILO_DATA_DIR=/data
ENV PILO_PORT=8080

CMD ["/app/pilo"]
