.PHONY: build build-linux test vet lint clean run \
       fe fe-install fe-dev serve

BIN := bin/moa

# ─── Go ────────────────────────────────────────────────────

build: fe
	go build -o $(BIN) ./cmd/moa

build-linux: fe
	GOOS=linux GOARCH=amd64 go build -o bin/moa-linux-amd64 ./cmd/moa

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	@echo "lint OK"

clean:
	rm -rf bin/
	rm -rf pkg/serve/static/build
	rm -f pkg/serve/static.publish.lock*
	rm -f pkg/serve/static/app.js pkg/serve/static/app.css

run: build
	./$(BIN) $(ARGS)

# ─── Frontend ──────────────────────────────────────────────

fe-install:
	cd pkg/serve/frontend && npm install

fe:
	cd pkg/serve/frontend && npm run build

# Dev mode: serve the build output from disk so a rebuild shows up on reload.
# Run `cd pkg/serve/frontend && npm run build -- --watch` in another terminal.
fe-dev:
	MOA_SERVE_STATIC_DIR=pkg/serve/static ./$(BIN) serve --port 8899

# Build everything and start the server.
serve: build
	./$(BIN) serve --port 8899
