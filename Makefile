# Makefile проекта GophProfile.
#
# Исполняемые файлы Go могут отсутствовать в PATH. Путь к go задаётся
# переменной GO: make test GO=/home/ellar/work/go_sdk/go1.26.2/bin/go

GO ?= go

# go test -coverprofile для пакетов без тестов вызывает go из PATH, поэтому каталог
# явно заданного GO добавляется в PATH.
ifneq ($(dir $(GO)),./)
export PATH := $(dir $(GO)):$(PATH)
endif
COVER_PROFILE ?= coverage.out
COVER_MIN ?= 50

# Версия, дата и коммит передаются в бинарные файлы через -ldflags.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN_DIR ?= bin
LDFLAGS = -s -w \
	-X main.buildVersion=$(VERSION) \
	-X main.buildDate=$(DATE) \
	-X main.buildCommit=$(COMMIT)

# Внешние инструменты запускаются без установки: mockery через go run,
# golangci-lint через docker-образ.
MOCKERY_VERSION ?= v3.9.0
GOLANGCI_IMAGE ?= golangci/golangci-lint:v2.15.0
COMPOSE ?= docker compose

.PHONY: all fmt vet lint lint-strict golangci test test-integration cover tidy mocks \
	build build-server build-worker up down

all: fmt vet lint test

## fmt: форматирует исходный код
fmt:
	$(GO) fmt ./...

## vet: базовая статика
vet:
	$(GO) vet ./...

## lint: мультичекер проекта. Замечания по файлам из go-build cache (os.Exit в
## сгенерированных main стандартной библиотеки) и по сгенерированным моками
## отфильтрованы; цель падает, если остались замечания по коду проекта.
lint:
	@out=$$($(GO) run ./cmd/staticlint ./... 2>&1 | grep -v -e '/go-build/' -e 'mock_.*\.go:' -e '^exit status'); \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## lint-strict: мультичекер со строгим набором (shadow, fieldalignment).
lint-strict:
	@STATICLINT_STRICT=1 $(MAKE) --no-print-directory lint

## golangci: golangci-lint по конфигурации .golangci.yml (нужен docker)
golangci:
	docker run --rm -v $(CURDIR):/app -w /app $(GOLANGCI_IMAGE) golangci-lint run ./...

## test: юнит-тесты с проверкой гонок
test:
	$(GO) test -race ./...

## test-integration: интеграционные тесты на testcontainers (нужен docker)
test-integration:
	$(GO) test -race -tags=integration -count=1 ./...

## cover: покрытие юнит-тестами и проверка порога COVER_MIN, %.
## Сгенерированные моки из профиля исключаются.
cover:
	$(GO) test -coverprofile=$(COVER_PROFILE) ./...
	@grep -v 'mock_.*\.go:' $(COVER_PROFILE) > $(COVER_PROFILE).tmp && mv $(COVER_PROFILE).tmp $(COVER_PROFILE)
	@$(GO) tool cover -func=$(COVER_PROFILE) | tail -n 1
	@$(GO) tool cover -func=$(COVER_PROFILE) | awk -v min=$(COVER_MIN) \
		'/^total:/ { gsub("%", "", $$3); if ($$3 + 0 < min) { printf "покрытие %s%% ниже порога %s%%\n", $$3, min; exit 1 } }'

## tidy: приводит go.mod и go.sum в порядок
tidy:
	$(GO) mod tidy

## mocks: генерирует моки интерфейсов по конфигурации .mockery.yaml
mocks:
	$(GO) run github.com/vektra/mockery/v3@$(MOCKERY_VERSION)

## build: собирает сервер и воркер
build: build-server build-worker

## build-server: собирает HTTP-сервер в $(BIN_DIR)/server (cgo нужен для WebP)
build-server:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/server ./cmd/server

## build-worker: собирает воркер в $(BIN_DIR)/worker (cgo нужен для WebP)
build-worker:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/worker ./cmd/worker

## up: поднимает окружение в docker compose
up:
	$(COMPOSE) up -d --build

## down: останавливает окружение docker compose
down:
	$(COMPOSE) down
