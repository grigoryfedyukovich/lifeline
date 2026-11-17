.PHONY: all build test race bench experiments perfcheck modcheck ci check vet demo release release-bench benchmark-validator versioncheck clean

EXPERIMENT_OUT ?= /tmp/lifeline-experiments-$(shell date -u +%Y%m%dT%H%M%SZ)
RELEASE_BENCH_OUT ?= /tmp/lifeline-release-benchmark-$(shell date -u +%Y%m%dT%H%M%SZ)

all: check build

build:
	go build -mod=readonly -trimpath ./...
	mkdir -p bin
	go build -mod=readonly -trimpath -o bin/lifeline ./cmd/lifeline

test:
	go test -mod=readonly -count=1 ./...

race:
	go test -mod=readonly -race -count=1 ./...

bench:
	go test -mod=readonly -run '^$$' -bench 'BenchmarkBuild' -benchmem ./internal/frontend

experiments: build
	python3 -B -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/run_experiments.py --output "$(EXPERIMENT_OUT)" --net-http

perfcheck:
	go test -mod=readonly -count=1 -run '^TestFrontendWorkBudgets$$' ./internal/frontend

modcheck:
	go mod tidy -diff

check: test
	go vet -mod=readonly ./analyzer ./cmd/lifeline ./internal/...

ci:
	./scripts/validate_ci.sh

release:
	./scripts/validate_release.sh

release-bench:
	./scripts/benchmark_release.sh --output "$(RELEASE_BENCH_OUT)"

benchmark-validator:
	./scripts/benchmark_release.sh --self-test

versioncheck: build
	./scripts/check_version_consistency.sh ./bin/lifeline

vet: build
	@go vet -vettool="$$(pwd)/bin/lifeline" ./...; code=$$?; \
	if [ $$code -ne 0 ] && [ $$code -ne 1 ]; then exit $$code; fi

demo: build
	./bin/lifeline ./examples/ignored_context ./examples/lost_cancel ./examples/proper_errgroup

clean:
	rm -rf bin
