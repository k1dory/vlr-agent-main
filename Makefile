.PHONY: build vet test tidy run up down logs ps clean image

BIN := vlr-main-agent

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/vlr-main-agent

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

image:
	docker build -t vlr-main-agent:latest .

# --- compose stack (run from repo root) ---
COMPOSE := docker compose -f deploy/compose/docker-compose.yml

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f agent

ps:
	$(COMPOSE) ps

clean:
	rm -f $(BIN)
