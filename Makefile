.PHONY: build test check image codex-image integration clean
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/reviewd ./cmd/reviewd
test:
	go test -race ./...
check: test
	go vet ./...
	python3 -m unittest discover -s benchmarks -p 'test_*.py'
image:
	docker build --target server -t reviewd:local .
codex-image:
	docker build --target codex -t reviewd-codex:local .
integration:
	REVIEWD_DOCKER_TEST=1 go test -v -count=1 ./internal/sandbox
clean:
	rm -rf bin

.PHONY: bench-build bench-test
bench-build: build
	go build -trimpath -o bin/reviewbench ./cmd/reviewbench
bench-test:
	go test -race ./internal/benchmark
	python3 -m unittest discover -s benchmarks -p 'test_*.py'
