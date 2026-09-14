# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY egress ./egress
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/reviewd ./cmd/reviewd \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/egress ./egress

# Base for operator-provided harnesses. /bin/sh and cp are required.
FROM debian:bookworm-slim AS harness
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git ripgrep && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/reviewd /usr/local/bin/reviewd

FROM node:22-bookworm-slim AS codex
ARG CODEX_VERSION=0.154.0
ARG EGRESS_CA_FILE=deploy/egress-ca-stub
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git ripgrep && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex@${CODEX_VERSION}
COPY ${EGRESS_CA_FILE} /tmp/egress-ca.pem
RUN if grep -q "BEGIN CERTIFICATE" /tmp/egress-ca.pem 2>/dev/null; then sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p' /tmp/egress-ca.pem > /usr/local/share/ca-certificates/egress-ca.crt && update-ca-certificates; fi; rm -f /tmp/egress-ca.pem
COPY --from=build /out/reviewd /usr/local/bin/reviewd
ENV HOME=/home/reviewd
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
WORKDIR /workspace

FROM debian:bookworm-slim AS egress
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates python3 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/reviewd /usr/local/bin/reviewd
COPY --from=build /out/egress /usr/local/bin/egress
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=3 CMD ["/usr/local/bin/egress", "healthcheck"]
CMD ["/usr/local/bin/egress", "serve", "--listen", ":8080"]

FROM codex AS server
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io python3 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/reviewd /usr/local/bin/reviewd
WORKDIR /
ENTRYPOINT ["reviewd"]
CMD ["serve", "--config", "/etc/reviewd/reviewd.json"]
