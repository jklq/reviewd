# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/reviewd ./cmd/reviewd

# Base for operator-provided harnesses. /bin/sh and cp are required.
FROM debian:bookworm-slim AS harness
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git ripgrep && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/reviewd /usr/local/bin/reviewd

FROM node:22-bookworm-slim AS codex
ARG CODEX_VERSION=0.154.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git ripgrep && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex@${CODEX_VERSION}
COPY --from=build /out/reviewd /usr/local/bin/reviewd
ENV HOME=/home/reviewd
WORKDIR /workspace

FROM codex AS server
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io python3 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/reviewd /usr/local/bin/reviewd
WORKDIR /
ENTRYPOINT ["reviewd"]
CMD ["serve", "--config", "/etc/reviewd/reviewd.json"]
