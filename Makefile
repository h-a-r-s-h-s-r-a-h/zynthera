# Zynthera developer commands. Run `make help` for the list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE := docker compose --env-file .env -f infra/docker-compose.yml
# The mock desktop container is only needed when the agent uses the mock driver.
PROFILE := $(shell grep -qs '^ZYN_VM_DRIVER=libvirt' .env || echo --profile mock)

.PHONY: help up down nuke migrate migrate-new seed generate dev test test-e2e lint fmt build env-check

help: ## Show this list
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  make %-14s %s\n", $$1, $$2}'

env-check:
	@test -f .env || { echo "No .env file. Run: cp .env.example .env"; exit 1; }

up: env-check ## Start Postgres, Redis, Mailpit, Adminer (and the mock desktop)
	$(COMPOSE) $(PROFILE) up -d --wait

down: env-check ## Stop the Compose services
	$(COMPOSE) --profile mock down

nuke: env-check ## Stop the Compose services and delete their data
	$(COMPOSE) --profile mock down --volumes

migrate: env-check ## Apply all database migrations
	go run ./cmd/api migrate up

migrate-new: ## Create a new migration: make migrate-new name=add_things
	@test -n "$(name)" || { echo "usage: make migrate-new name=add_things"; exit 1; }
	@./scripts/migrate-new.sh "$(name)"

seed: env-check ## Create the admin user, OS images and the local dev host
	go run ./cmd/api seed

generate: ## Regenerate the typed SQL code with sqlc
	cd db && sqlc generate

dev: env-check ## Run the API, the agent and the web app together
	@./scripts/dev.sh

test: ## Run Go tests and web unit tests
	# -p 1: packages share one test database, so they run one at a time.
	go test -p 1 ./...
	cd web && npm test

test-e2e: ## Run the Playwright end-to-end tests (mock mode)
	cd web && npm run test:e2e

lint: ## Run golangci-lint, eslint and prettier
	golangci-lint run ./...
	cd web && npm run lint && npm run format:check

fmt: ## Format Go and web code
	gofmt -w cmd internal db
	cd web && npm run format

build: ## Build the three binaries into bin/
	CGO_ENABLED=0 go build -trimpath -o bin/zynthera-api ./cmd/api
	CGO_ENABLED=0 go build -trimpath -o bin/zynthera-agent ./cmd/agent
	CGO_ENABLED=0 go build -trimpath -o bin/zynthera-file-bridge ./cmd/file-bridge
