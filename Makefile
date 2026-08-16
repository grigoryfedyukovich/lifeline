.PHONY: all build test race check vet demo clean

all: check build

build:
	mkdir -p bin
	go build -o bin/lifeline ./cmd/lifeline

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

check: test
	go vet ./analyzer ./cmd/lifeline ./internal/...

vet: build
	@go vet -vettool="$$(pwd)/bin/lifeline" ./...; code=$$?; \
	if [ $$code -ne 0 ] && [ $$code -ne 1 ]; then exit $$code; fi

demo: build
	./bin/lifeline ./examples/ignored_context ./examples/lost_cancel ./examples/proper_errgroup

clean:
	rm -rf bin
