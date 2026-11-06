BINARY := sysprune
LDFLAGS := -s -w

.PHONY: build test lint vet snapshot run clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sysprune

test:
	go test -race ./...

lint:
	staticcheck ./...

vet:
	go vet ./...

snapshot:
	goreleaser release --snapshot --clean

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
	rm -rf dist/
