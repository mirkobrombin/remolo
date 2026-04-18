BINARY := remolo
PKG := ./cmd/remolo
GOFLAGS :=

.PHONY: all build test vet fuzz fmt clean run-host tidy

all: vet test build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY) $(PKG)

test:
	go test -timeout 120s ./...

race:
	go test -race -timeout 180s ./...

vet:
	go vet ./...

fuzz:
	go test ./internal/token/ -run x -fuzz FuzzDecode -fuzztime 20s

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)
