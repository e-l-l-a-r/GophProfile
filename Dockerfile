# syntax=docker/dockerfile:1

# Сборка: cgo нужен для кодирования WebP (libwebp собирается из исходников
# пакета chai2010/webp), поэтому в образе сборки есть компилятор C.
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache build-base
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for app in server worker; do \
      CGO_ENABLED=1 go build -trimpath \
        -ldflags "-s -w -X main.buildVersion=${VERSION} -X main.buildCommit=${COMMIT} -X main.buildDate=${DATE}" \
        -o /out/$app ./cmd/$app || exit 1; \
    done

# Запуск: сервер и воркер в одном образе, нужная программа выбирается командой.
# Бинарники связаны с musl динамически, поэтому базовый образ - тоже alpine.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -H -u 10001 app
COPY --from=builder /out/server /out/worker /usr/local/bin/
USER app
EXPOSE 8080
ENV ADDRESS=0.0.0.0:8080
CMD ["server"]
