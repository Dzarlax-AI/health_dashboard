.PHONY: dev build migrate dedup backfill backfill-force import docker-up docker-down test

DB_PATH ?= ./data/health.db
ADDR    ?= :8080

dev:
	mkdir -p data
	DB_PATH=$(DB_PATH) ADDR=$(ADDR) go run ./cmd/server

build:
	CGO_ENABLED=1 go build -o bin/server ./cmd/server

migrate:
	DB_PATH=$(DB_PATH) go run ./cmd/migrate

dedup:
	DB_PATH=$(DB_PATH) go run ./cmd/dedup

backfill:
	DB_PATH=$(DB_PATH) go run ./cmd/backfill

backfill-force:
	DB_PATH=$(DB_PATH) go run ./cmd/backfill --force

import:
	CGO_ENABLED=1 go run ./cmd/import --db $(DB_PATH) --file $(FILE) --batch 500 --pause 150ms

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

test:
	curl -s -X POST http://localhost$(ADDR)/health \
		-H "Content-Type: application/json" \
		-H "automation-name: Test" \
		-H "automation-id: test-001" \
		-H "session-id: sess-001" \
		-d '{"data":[{"name":"HKQuantityTypeIdentifierStepCount","units":"count","data":[{"date":"2026-03-04 00:00:00 +0000","qty":8234}]}]}' \
		| python3 -m json.tool
