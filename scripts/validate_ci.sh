#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

printf 'validation: toolchain\n'
go version
printf 'validation: contract\n'
./scripts/check_validation_contract.sh
printf 'validation: module files\n'
go mod tidy -diff
printf 'validation: markdown links\n'
./scripts/check_markdown_links.sh
printf 'validation: performance claims\n'
./scripts/check_performance_claims.sh
printf 'validation: tests\n'
go test -mod=readonly -count=1 ./...
printf 'validation: vet\n'
go vet -mod=readonly ./analyzer ./cmd/lifeline ./internal/...
printf 'validation: race\n'
go test -mod=readonly -race -count=1 ./...
printf 'validation: algorithmic work budgets\n'
go test -mod=readonly -count=1 -run '^TestFrontendWorkBudgets$' ./internal/frontend
printf 'validation: benchmark smoke\n'
go test -mod=readonly -run '^$' -bench '^BenchmarkBuild' -benchtime=1x ./internal/frontend
printf 'validation: build\n'
go build -mod=readonly -trimpath ./...
mkdir -p bin
go build -mod=readonly -trimpath -o bin/lifeline ./cmd/lifeline
printf 'validation: release identity\n'
./scripts/check_version_consistency.sh ./bin/lifeline
printf 'validation: benchmark evidence validator\n'
./scripts/benchmark_release.sh --self-test
printf 'validation: dependency vulnerabilities\n'
./scripts/check_vulnerabilities.sh
printf 'validation: PASS\n'
