.PHONY: run dev web snippet build test test-go test-web docker up down clean

# Run the Go server locally (UI must be built first, or use `make dev` + vite).
run:
	cd server && GLANCE_DATABASE_PATH=../data/glance.db go run ./cmd/glance

# Start the Vite dev server (proxies /api to :8080).
dev:
	cd server/web && npm run dev

# Build the web UI into the Go embed directory.
web:
	cd server/web && npm ci && npm run build

# Minify the tracking snippet from its readable source. glance.js is committed,
# so this only needs running when glance.src.js changes.
snippet:
	cd server && ./web/node_modules/.bin/rolldown internal/api/glance.src.js \
		--format iife --minify -o internal/api/glance.js
	@printf 'snippet: %s bytes\n' "$$(wc -c < server/internal/api/glance.js | tr -d ' ')"

# Build a single binary with the UI embedded.
build: web
	cd server && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/glance ./cmd/glance

test: test-go test-web

test-go:
	cd server && go vet ./... && go test ./...

test-web:
	cd server/web && npm run check && npx vitest run

docker:
	docker compose build

up:
	docker compose up -d

down:
	docker compose down

clean:
	rm -rf bin data server/internal/web/dist/assets server/internal/web/dist/index.html server/web/node_modules
