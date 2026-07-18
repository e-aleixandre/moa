.PHONY: build build-linux test vet lint clean run \
       fe fe-install fe-dev fe-next fe-next-install serve

BIN := bin/moa

# ─── Go ────────────────────────────────────────────────────

build: fe fe-next
	go build -o $(BIN) ./cmd/agent

build-linux: fe fe-next
	GOOS=linux GOARCH=amd64 go build -o bin/moa-linux-amd64 ./cmd/agent

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	@echo "lint OK"

clean:
	rm -rf bin/
	rm -f pkg/serve/static/app.js pkg/serve/static/app.css
	rm -f pkg/serve/static-next/app.js pkg/serve/static-next/app.css

run: build
	./$(BIN) $(ARGS)

# ─── Frontend ──────────────────────────────────────────────

fe-install:
	cd pkg/serve/frontend && npm install

fe:
	cd pkg/serve/frontend && npm run build

fe-next-install:
	cd pkg/serve/frontend-next && npm install

fe-next:
	cd pkg/serve/frontend-next && npm run build

# Dev mode: serve static from disk so esbuild changes appear on reload.
# Run `make fe` in another terminal after editing src/.
fe-dev:
	MOA_SERVE_STATIC_DIR=pkg/serve/frontend/src ./$(BIN) serve --port 8899

# Build everything and start the server.
serve: build
	./$(BIN) serve --port 8899
