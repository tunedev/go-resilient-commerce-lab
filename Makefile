MODULE := github.com/tunedev/go-resilient-commerce-lab
COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: fmt lint vet test test-race integration run docker-up docker-down scenario site

fmt:
	golangci-lint fmt ./...

lint:
	golangci-lint config verify
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

integration:
	go test -race -tags=integration ./...

run:
	go run ./cmd/order

docker-up:
	$(COMPOSE) up -d --build

docker-down:
	$(COMPOSE) down -v

scenario:
	go run ./cmd/labctl scenario $(NAME)

site:
	cd docs && hugo server -D
