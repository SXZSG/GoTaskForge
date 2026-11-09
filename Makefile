.PHONY: run test race vet build build-mysql lint-up docker-up docker-down docker-logs
run:
	go run ./cmd/server

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -o bin/taskforge ./cmd/server

build-mysql:
	mkdir -p bin
	go mod download
	go build -tags mysql -o bin/taskforge-mysql ./cmd/server

lint-up:
	@test -z "$$(gofmt -l ./cmd ./internal)" || (echo "Go files are not gofmt-clean"; exit 1)

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs --tail=100 api
