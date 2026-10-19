# The agent box: the filesystem an agent session is allowed to see.
#
# What is NOT here is the point. No host home, no ~/.ssh, no docker socket, no
# inherited daemon environment — the console mounts exactly the project
# directory and a per-box state volume, and forwards environment by name.
# --- abduco (builder stage) ---
# abduco is not packaged for bookworm (abduco 0.6-1 exists in forky/sid only), so
# it is built from the upstream release on the same base image, and only the
# binary is copied into the final image. Pinned by version AND SHA256:
#   https://github.com/martanne/abduco/releases/download/v0.6/abduco-0.6.tar.gz
# To bump: change ABDUCO_VERSION, download the tarball, recompute the sum and
# compare it with Homebrew's Formula/a/abduco.rb.
FROM node:24-bookworm-slim AS abduco-build
ARG ABDUCO_VERSION=0.6
ARG ABDUCO_SHA256=c90909e13fa95770b5afc3b59f311b3d3d2fdfae23f9569fa4f96a3e192a35f4
RUN apt-get update && apt-get install -y --no-install-recommends \
      gcc make libc6-dev curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    curl -fsSL "https://github.com/martanne/abduco/releases/download/v${ABDUCO_VERSION}/abduco-${ABDUCO_VERSION}.tar.gz" -o /tmp/abduco.tar.gz; \
    echo "${ABDUCO_SHA256}  /tmp/abduco.tar.gz" | sha256sum -c -; \
    tar -C /tmp -xzf /tmp/abduco.tar.gz; \
    make -C "/tmp/abduco-${ABDUCO_VERSION}"; \
    install -D -m 0755 "/tmp/abduco-${ABDUCO_VERSION}/abduco" /out/abduco

FROM node:24-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates ripgrep jq less procps curl unzip \
    && rm -rf /var/lib/apt/lists/*

# --- python3 ---
# scripts/lint.sh maps eslint's JSON report back to the source files with
# python3, so a box without it cannot pass scripts/test.sh. Debian bookworm's
# python3 package (no recommends); it moves with the base image's apt snapshot.
RUN apt-get update && apt-get install -y --no-install-recommends python3 \
    && rm -rf /var/lib/apt/lists/*

# Cloud tooling for bound sessions. Credentials never live in the image: the
# AWS CLI and terraform reach the agent-box broker through credential_process.
#
# AWS CLI v2 2.38.1, pinned by version AND per-arch SHA256 (computed once from
# the pinned download):
#   https://awscli.amazonaws.com/awscli-exe-linux-x86_64-2.38.1.zip
#   https://awscli.amazonaws.com/awscli-exe-linux-aarch64-2.38.1.zip
# To bump: change AWSCLI_VERSION, download both zips, recompute both sums.
ARG AWSCLI_VERSION=2.38.1
ARG AWSCLI_SHA256_X86_64=b11ed416d91f4251da363a17706938eeaf0e520745754c104c0c1890dd8363c7
ARG AWSCLI_SHA256_AARCH64=91360c0adb2ae508334ff657dce4787b598b443a11cf3dc58e08b74f12e16009
# Terraform 1.16.6, pinned by version AND per-arch SHA256, taken from (and
# cross-checked against the zips and) HashiCorp's SHA256SUMS:
#   https://releases.hashicorp.com/terraform/1.16.6/terraform_1.16.6_SHA256SUMS
# On a version bump the new sums MUST be checked against the signed
# SHA256SUMS(.sig) (HashiCorp release key), not just copied from the download.
ARG TERRAFORM_SHA256_AMD64=38034b38d03096399a7a1264a14561a8029b2b496fd1f169452656e53968f1b7
ARG TERRAFORM_SHA256_ARM64=a750b5a11038f165846b2658c40c88cd7070aa67a61bab431a14ba27928ea9ea
ARG TERRAFORM_VERSION=1.16.6
RUN set -eu; \
    arch="$(uname -m)"; \
    case "$arch" in \
      x86_64) sum="$AWSCLI_SHA256_X86_64" ;; \
      aarch64) sum="$AWSCLI_SHA256_AARCH64" ;; \
      *) echo "unsupported arch: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${arch}-${AWSCLI_VERSION}.zip" -o /tmp/awscli.zip; \
    echo "${sum}  /tmp/awscli.zip" | sha256sum -c -; \
    unzip -q /tmp/awscli.zip -d /tmp; /tmp/aws/install; rm -rf /tmp/aws /tmp/awscli.zip; \
    aws --version
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$TERRAFORM_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$TERRAFORM_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${a}.zip" -o /tmp/terraform.zip; \
    echo "${sum}  /tmp/terraform.zip" | sha256sum -c -; \
    unzip -q /tmp/terraform.zip -d /usr/local/bin; rm -f /tmp/terraform.zip; \
    terraform version

# --- Go ---
# Go 1.26.5, pinned by version AND per-arch SHA256 (from go.dev's release list):
#   https://go.dev/dl/go1.26.5.linux-amd64.tar.gz
#   https://go.dev/dl/go1.26.5.linux-arm64.tar.gz
# To bump: take the new version and sums from
#   https://go.dev/dl/?mode=json&include=all
# and keep GO_VERSION equal to the `go` directive in go.mod (a test enforces it).
ARG GO_VERSION=1.26.5
ARG GO_SHA256_AMD64=5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053
ARG GO_SHA256_ARM64=fe4789e92b1f33358680864bbe8704289e7bb5fc207d80623c308935bd696d49
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$GO_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$GO_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${a}.tar.gz" -o /tmp/go.tar.gz; \
    echo "${sum}  /tmp/go.tar.gz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; rm -f /tmp/go.tar.gz; \
    /usr/local/go/bin/go version
# Everything Go writes lives under the box home (a per-box bind mount). The
# directories are NOT created here: the home is a bind mount, so nothing the
# image creates under /home/agent is visible at run time. Go creates GOCACHE and
# GOMODCACHE itself; GOTMPDIR must exist, so the entrypoint creates it.
# GOTOOLCHAIN=local makes a go.mod asking for a newer Go fail loudly instead of
# downloading an unpinned toolchain through the proxy.
ENV GOPATH=/home/agent/go
ENV GOMODCACHE=/home/agent/go/pkg/mod
ENV GOCACHE=/home/agent/.cache/go-build
ENV GOTMPDIR=/home/agent/.cache/go-tmp
ENV GOTOOLCHAIN=local
# The module cache must stay deletable so the box home is reclaimed (Go makes it read-only by default).
ENV GOFLAGS=-modcacherw
ENV PATH=/usr/local/go/bin:/home/agent/go/bin:$PATH

# --- abduco ---
# Built in the abduco-build stage above (see there for source, pin and bump).
COPY --from=abduco-build /out/abduco /usr/local/bin/abduco
RUN abduco -v 2>&1 | grep -q 'abduco-0.6'

# --- codebase-memory-mcp ---
# v0.10.8, pinned by version AND per-arch SHA256 of the release's *-portable build:
#   https://github.com/DeusData/codebase-memory-mcp/releases/download/v0.10.8/codebase-memory-mcp-linux-amd64-portable.tar.gz
#   https://github.com/DeusData/codebase-memory-mcp/releases/download/v0.10.8/codebase-memory-mcp-linux-arm64-portable.tar.gz
# Why portable: the release's plain linux build is dynamically linked and needs
# GLIBC_2.38, but bookworm ships 2.36. The portable build is statically linked,
# so it runs here; the build asserts that. The host's `codebase-memory-mcp
# --version` is the source of truth for which version to pin.
# To bump: take the sums from the release's checksums.txt, cross-checked against
# the GitHub asset digests, then change CBM_VERSION.
ARG CBM_VERSION=0.10.8
ARG CBM_SHA256_AMD64=6eef49652bc0c7820f43114125044d40bf7f4d97c11b2592f6b0f6a307702325
ARG CBM_SHA256_ARM64=5697d986d9716c913163b4bff7b3a294287f3b843e993bc1ff71e78dcdc21781
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$CBM_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$CBM_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/DeusData/codebase-memory-mcp/releases/download/v${CBM_VERSION}/codebase-memory-mcp-linux-${a}-portable.tar.gz" -o /tmp/cbm.tar.gz; \
    echo "${sum}  /tmp/cbm.tar.gz" | sha256sum -c -; \
    mkdir /tmp/cbm; tar -C /tmp/cbm -xzf /tmp/cbm.tar.gz; \
    install -m 0755 /tmp/cbm/codebase-memory-mcp /usr/local/bin/codebase-memory-mcp; \
    rm -rf /tmp/cbm /tmp/cbm.tar.gz; \
    ldd /usr/local/bin/codebase-memory-mcp 2>&1 | grep -q 'not a dynamic executable'; \
    codebase-memory-mcp --version | grep -q "${CBM_VERSION}"

RUN npm install -g @anthropic-ai/claude-code

# --- Playwright MCP + Chromium ---
# Debian's chromium (apt-signed; security fixes arrive with every rebuild, and
# there is no unhashed CDN download; the version moves with the base image's apt
# snapshot, 155.0.8059.79 when measured, +278 MB) and @playwright/mcp from npm,
# pinned by version. The package's binary is playwright-mcp, not
# mcp-server-playwright. To bump: change PLAYWRIGHT_MCP_VERSION to a published
# @playwright/mcp release, rebuild, and re-run the A3 browser measurement.
# Findings this rests on (docs/superpowers/notes/2026-10-19-a3-measure.md):
# Playwright already launches chromium with --no-sandbox and
# --disable-dev-shm-usage, so no MCP --config file is needed, and a 64 MB
# /dev/shm was enough. No browser-path variable is set: Playwright's bundled
# browser is not used. The server is declared in /etc/agent-box/mcp.json
# below.
ARG PLAYWRIGHT_MCP_VERSION=0.0.83
RUN apt-get update && apt-get install -y --no-install-recommends chromium \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION} \
    && chromium --version \
    && playwright-mcp --version | grep -q "${PLAYWRIGHT_MCP_VERSION}"

# The console always runs the box with `--user <daemon uid>` and bind-mounts a
# host-owned directory as the home, so ownership is correct on any machine and
# these values never decide anything at run time. They exist only so the image
# is usable standalone; do NOT rely on them matching your host user.
ARG UID=1001
ARG GID=1001
RUN groupadd -g "$GID" agent \
    && useradd -m -u "$UID" -g "$GID" -s /bin/bash agent \
    && mkdir -p /home/agent/.claude \
    && chown -R "$UID:$GID" /home/agent

# MCP servers the image itself provides. agent-box merges this file into the
# session's MCP bundle (it is read by a probe run with --entrypoint sh). No
# "env": the default cache $HOME/.cache/codebase-memory-mcp is already in the
# box home.
# playwright writes its output to /tmp/playwright-mcp (dies with the box, never
# in a clone's working tree) and starts every browser with an in-memory profile
# (--isolated), so no login persists in the box home.
RUN mkdir -p /etc/agent-box && printf '%s\n' '{"mcpServers":{"codebase-memory":{"type":"stdio","command":"/usr/local/bin/codebase-memory-mcp","args":[]},"playwright":{"type":"stdio","command":"/usr/local/bin/playwright-mcp","args":["--headless","--isolated","--output-dir","/tmp/playwright-mcp","--browser","chromium","--executable-path","/usr/bin/chromium"]}}}' > /etc/agent-box/mcp.json && jq -e '.mcpServers["codebase-memory"].command' /etc/agent-box/mcp.json && jq -e '.mcpServers["playwright"].command' /etc/agent-box/mcp.json

# Entrypoint: create $GOTMPDIR at container start, then exec the arguments. The
# box home is a bind mount of the box's state dir, so the directory cannot be
# made at build time, and Go refuses a missing GOTMPDIR (spec correction 5).
# agent-box always passes `sleep infinity`; the probes override --entrypoint.
RUN printf '%s\n' '#!/bin/sh' '# Go refuses a missing GOTMPDIR; the home is a bind mount, so make it here.' '[ -n "${GOTMPDIR:-}" ] && mkdir -p "$GOTMPDIR" 2>/dev/null' 'exec "$@"' > /usr/local/bin/agent-box-entrypoint && chmod 0755 /usr/local/bin/agent-box-entrypoint

USER agent
ENV HOME=/home/agent
ENV SHELL=/bin/bash
WORKDIR /home/agent
# The browser and the MCP must run as the box's own user, not only as root.
RUN chromium --version && playwright-mcp --version | grep -q "${PLAYWRIGHT_MCP_VERSION}"
ENTRYPOINT ["/usr/local/bin/agent-box-entrypoint"]
CMD ["bash"]
