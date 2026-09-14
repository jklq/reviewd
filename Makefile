.PHONY: build test check image codex-image egress-image integration clean
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/reviewd ./cmd/reviewd
test:
	go test -race ./...
check: test
	go vet ./...
image:
	docker build --target server -t reviewd:local .
codex-image:
	docker build --target codex -t reviewd-codex:local .
egress-image:
	docker build --target egress -t reviewd-egress:local .
integration: egress-image
	REVIEWD_DOCKER_TEST=1 go test -v -count=1 ./internal/sandbox
clean:
	rm -rf bin
