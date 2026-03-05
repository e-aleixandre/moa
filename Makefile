.PHONY: build test vet lint clean

MODULE := github.com/ealeixandre/go-agent
BIN    := bin/agent

build:
	go build -o $(BIN) ./cmd/agent

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
