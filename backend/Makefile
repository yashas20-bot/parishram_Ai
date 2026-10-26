.PHONY: sqlc-generate migrate-up migrate-down run test build

DB_URL ?= postgres://postgres:postgres@localhost:5432/parishram_ai?sslmode=disable
MIGRATIONS_PATH = db/migrations

sqlc-generate:
	sqlc generate

migrate-up:
	migrate -path $(MIGRATIONS_PATH) -database "$(DB_URL)" up

migrate-down:
	migrate -path $(MIGRATIONS_PATH) -database "$(DB_URL)" down 1

test:
	go test -v ./...

build:
	go build -o bin/server cmd/server/main.go

run:
	go run cmd/server/main.go
