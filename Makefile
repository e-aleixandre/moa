.PHONY: build build-linux test vet lint clean

MODULE := github.com/ealeixandre/go-agent
BIN    := bin/agent

build:
	go build -o $(BIN) ./cmd/agent

# Cross-compile for Linux amd64 (container deployment)
build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/moa-linux-amd64 ./cmd/agent

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	@echo "lint OK"

clean:
	rm -rf bin/

run: build
	./$(BIN) $(ARGS)
