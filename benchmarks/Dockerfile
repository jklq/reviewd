FROM reviewd-opencode:local
USER root
RUN apt-get update && apt-get install -y --no-install-recommends jq && rm -rf /var/lib/apt/lists/*
COPY opencode-bounded.sh /usr/local/bin/reviewbench-bounded
RUN chmod 0755 /usr/local/bin/reviewbench-bounded
