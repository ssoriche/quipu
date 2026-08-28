BIN := quipu
build:
	go build -o $(BIN) ./cmd/$(BIN)
test:
	go test ./...
vet:
	go vet ./...
fmt:
	gofmt -l -w .
lint: vet
	test -z "$$(gofmt -l .)"
install:
	go build -o $(HOME)/.local/bin/$(BIN) ./cmd/$(BIN)
check: lint test build
.PHONY: build test vet fmt lint install check
