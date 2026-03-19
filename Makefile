.PHONY: build run test lint up down logs

build:
	go build -o bin/llm-gateway .

run:
	go run .

test:
	go test ./...

lint:
	golangci-lint run ./...

up:
	cp -n .env.example .env 2>/dev/null || true
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f gateway
