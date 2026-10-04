BIN := bin/ratelimiter
VERSION ?= dev

.PHONY: all build run test race bench fmt vet clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) .

run:
	go run -ldflags "-X main.version=$(VERSION)" .

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

bench:
	go test -run '^$$' -bench . -benchmem ./internal/limiter

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -rf bin
