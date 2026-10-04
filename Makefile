BIN := bin/ratelimiter
VERSION ?= dev

.PHONY: all build dist docker run test race bench fmt vet clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) .

# Static Linux binaries for both architectures, named as the release assets
# are. The Ansible playbook copies these when no release version is set.
dist:
	@for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/ratelimiter_linux_$$arch . && echo "built bin/ratelimiter_linux_$$arch"; \
	done

# Local container image (same Dockerfile CI uses).
docker:
	docker build --build-arg VERSION=$(VERSION) -t ratelimiter:$(VERSION) .

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
