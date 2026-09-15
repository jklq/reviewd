.PHONY: build test check image codex-image integration clean
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/reviewd ./cmd/reviewd
	CGO_ENABLED=0 go build -trimpath -o bin/reviewd-credential-codex ./cmd/reviewd-credential-codex
	CGO_ENABLED=0 go build -trimpath -o bin/reviewd-credential-project ./cmd/reviewd-credential-project
test:
	go test -race ./...
check: test
	go vet ./...
image:
	docker build --target server -t reviewd:local .
codex-image:
	docker build --target codex -t reviewd-codex:local .
integration:
	REVIEWD_DOCKER_TEST=1 go test -v -count=1 ./internal/sandbox
clean:
	rm -rf bin

.PHONY: claudecode-image opencode-image muse-image
claudecode-image:
	docker build -f Dockerfile.providers --target claudecode -t reviewd-claudecode:local .
opencode-image:
	docker build -f Dockerfile.providers --target opencode -t reviewd-opencode:local .
muse-image:
	test -n "$(MUSE_BINARY)"
	mkdir -p bin
	cp "$(MUSE_BINARY)" bin/muse
	docker build -f Dockerfile.providers --target muse -t reviewd-muse:local .

.PHONY: drivers
drivers:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/ ./cmd/reviewd-driver-codex ./cmd/reviewd-driver-claudecode ./cmd/reviewd-driver-muse ./cmd/reviewd-driver-opencode

.PHONY: sdk-integration
sdk-integration:
	REVIEWD_SDK_TEST=1 go test -v -count=1 ./harness -run TestSDKContracts
