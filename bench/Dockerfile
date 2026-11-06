FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git curl openssh-client ripgrep python3 jq less sqlite3 && rm -rf /var/lib/apt/lists/*
COPY oc.tgz /tmp/oc.tgz
RUN tar xzf /tmp/oc.tgz -C /usr/local/bin && rm /tmp/oc.tgz && chmod +x /usr/local/bin/opencode && opencode --version
COPY explorerepo /opt/explorerepo/explorerepo
COPY entry.sh /usr/local/bin/entry.sh
RUN useradd -m dev
USER dev
WORKDIR /home/dev/work
