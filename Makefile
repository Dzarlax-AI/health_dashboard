.PHONY: dev build backfill backfill-force import docker-up docker-down test

ADDR ?= :8080

dev:
	DATABASE_URL=$(DATABASE_URL) ADDR=$(ADDR) go run ./cmd/server

build:
	CGO_ENABLED=0 go build -o bin/server ./cmd/server

backfill:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/backfill

backfill-force:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/backfill --force

import:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/import --file $(FILE) --batch 500 --pause 150ms

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
		-d '{"data":{"metrics":[{"name":"step_count","units":"count","data":[{"date":"2026-03-04 00:00:00 +0000","qty":8234,"source":"iPhone"}]}]}}' \
		| python3 -m json.tool
