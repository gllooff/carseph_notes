.PHONY: build test vet run invite compose-up compose-down deploy

# Build local binary.
build:
	go build -trimpath -ldflags="-s -w" -o bin/notes ./cmd/notes

test:
	go test ./...

vet:
	go vet ./...

# Run locally (no Docker) at http://localhost:8080.
run: build
	DEV=true DATA_DIR=./data ./bin/notes serve

# Mint an invite code against ./data.
invite:
	DEV=true DATA_DIR=./data ./bin/notes invite-new

# Local verification via Docker Compose.
# The container runs as host uid (user: 1000:1000) so ./data stays host-owned.
compose-up:
	docker compose up --build -d
	@echo "App at http://localhost:8085 — mint a code: docker compose exec notes /notes invite-new"

compose-down:
	docker compose down

# --- production (Droplet, systemd, no Docker) ---

BIN := bin/notes_linux_amd64
HOST := root@137.184.22.193

build-prod:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/notes

# Cross-compile, ship, restart. First-time setup: see DESIGN.md §10.
deploy: build-prod
	scp $(BIN) $(HOST):/usr/local/bin/notes.new
	ssh $(HOST) 'mv /usr/local/bin/notes.new /usr/local/bin/notes && systemctl restart notes && systemctl is-active notes'
	curl -fsS https://notes.jys-reality.win/api/healthz
