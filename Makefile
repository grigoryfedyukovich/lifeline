.PHONY: all build test race bench experiments perfcheck modcheck ci check vet demo clean

EXPERIMENT_OUT ?= /tmp/lifeline-experiments-$(shell date -u +%Y%m%dT%H%M%SZ)

all: check build

build:
	go build ./...
	mkdir -p bin
	go build -o bin/lifeline ./cmd/lifeline

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

bench:
	go test -run '^$$' -bench 'BenchmarkBuild' -benchmem ./internal/frontend

experiments: build
	python3 -B -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/run_experiments.py --output "$(EXPERIMENT_OUT)" --net-http

perfcheck:
	go test -count=1 -run '^TestFrontendWorkBudgets$$' ./internal/frontend

modcheck:
	go mod tidy -diff

ci: modcheck check race perfcheck build
	go test -run '^$$' -bench 'BenchmarkBuild' -benchtime=1x ./internal/frontend

check: test
	go vet ./analyzer ./cmd/lifeline ./internal/...

vet: build
	@go vet -vettool="$$(pwd)/bin/lifeline" ./...; code=$$?; \
	if [ $$code -ne 0 ] && [ $$code -ne 1 ]; then exit $$code; fi

demo: build
	./bin/lifeline ./examples/ignored_context ./examples/lost_cancel ./examples/proper_errgroup

clean:
	rm -rf bin
