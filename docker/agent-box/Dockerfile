# The agent box: the filesystem an agent session is allowed to see.
#
# What is NOT here is the point. No host home, no ~/.ssh, no docker socket, no
# inherited daemon environment — the console mounts exactly the project
# directory and a per-box state volume, and forwards environment by name.
# --- base image ---
# node:24-bookworm-slim, pinned by the INDEX digest (the multi-arch manifest list,
# so one pin covers amd64 and arm64). Tag and digest read on 2026-10-19 with
#   docker buildx imagetools inspect node:24-bookworm-slim
# Every stage must carry the same digest (a test enforces it). Bumping the base is
# a manual edit of both FROM lines, which changes the Dockerfile hash on purpose.
# RESIDUAL, accepted: apt packages (git, python3, chromium, make, ...)
# follow the base image's apt snapshot, so they are only as pinned as the digest;
# every non-apt download below is pinned by version AND sha256.
# --- abduco (builder stage) ---
# abduco is not packaged for bookworm (abduco 0.6-1 exists in forky/sid only), so
# it is built from the upstream release on the same base image, and only the
# binary is copied into the final image. Pinned by version AND SHA256:
#   https://github.com/martanne/abduco/releases/download/v0.6/abduco-0.6.tar.gz
# To bump: change ABDUCO_VERSION, download the tarball, recompute the sum and
# compare it with Homebrew's Formula/a/abduco.rb.
FROM node:24-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS abduco-build
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

# --- HashiCorp release-signing key (builder stage) ---
# tfenv checks the signature on a terraform release's SHA256SUMS only against a
# keyring we hand it. This stage fetches HashiCorp's key and turns it into that
# keyring; gnupg stays in this stage and never reaches the final image.
#   key:  https://www.hashicorp.com/.well-known/pgp-key.txt
#   fpr:  C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F (key id 72D7468F)
# The fingerprint was cross-checked against https://www.hashicorp.com/trust/security
# ("release archives") and against the .72D7468F suffix tfenv looks for on
# terraform_<v>_SHA256SUMS.72D7468F.sig; the same key's signatures were verified on
# a real release with the resulting keyring. The file is pinned by sha256 AND the
# build asserts the primary key's fingerprint equals HASHICORP_PGP_FPR.
# HashiCorp extending the key's expiry changes the file (and so this sum): that is
# a manual bump, not a failure to work around. (tfenv's own bundled copy of the
# key expired on 2026-04-18, which is why it is replaced.)
FROM node:24-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS hashicorp-key
ARG HASHICORP_PGP_FPR=C874011F0AB405110D02105534365D9472D7468F
ARG HASHICORP_PGP_SHA256=c2f5bc1163bd8d15a711616b587bcede212d045a5b8b52df01c74095897cd065
RUN apt-get update && apt-get install -y --no-install-recommends gnupg curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    curl -fsSL "https://www.hashicorp.com/.well-known/pgp-key.txt" -o /tmp/hashicorp-key.txt; \
    echo "${HASHICORP_PGP_SHA256}  /tmp/hashicorp-key.txt" | sha256sum -c -; \
    keys="$(gpg --show-keys --with-colons /tmp/hashicorp-key.txt)"; \
    [ "$(printf '%s\n' "$keys" | grep -c '^pub:')" = 1 ]; \
    fpr="$(printf '%s\n' "$keys" | awk -F: '$1=="pub"{p=1} p&&$1=="fpr"{print $10; exit}')"; \
    [ "$fpr" = "${HASHICORP_PGP_FPR}" ]; \
    mkdir -p /out; gpg --dearmor -o /out/hashicorp-keys.pgp /tmp/hashicorp-key.txt; \
    [ -s /out/hashicorp-keys.pgp ]

FROM node:24-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5

# Links the published ghcr package to this repository.
LABEL org.opencontainers.image.source="https://github.com/culiops/agent-box"

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
# @playwright/mcp release, rebuild, and re-check that the browser starts inside a box.
# Findings this rests on: Playwright already launches chromium with --no-sandbox and
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

# Entrypoint: at container start, create $GOTMPDIR, link the tflint default config
# and plugin into the home, and seed and re-arm the tfenv state in the home (see
# the tfenv block), then exec the arguments. The box home is a bind mount of the
# box's state dir and persists across image bumps, so none of this can be made at
# build time, and every step must also repair what an EARLIER image left behind.
# Go refuses a missing GOTMPDIR, hence its mkdir.
# agent-box always passes `sleep infinity`; the probes override --entrypoint.
RUN printf '%s\n' '#!/bin/sh' '# Go refuses a missing GOTMPDIR; the home is a bind mount, so make it here.' '[ -n "${GOTMPDIR:-}" ] && mkdir -p "$GOTMPDIR" 2>/dev/null' '# tflint AWS ruleset: link the default config unless the home holds a REGULAR file (the user-owned one; a repo .tflint.hcl still wins). A symlink, dangling or not, is ours from an earlier start: re-point it, so a ruleset bump is picked up.' 'if [ ! -e "$HOME/.tflint.hcl" ] || [ -L "$HOME/.tflint.hcl" ]; then rm -f "$HOME/.tflint.hcl"; ln -s /etc/agent-box/tflint.hcl "$HOME/.tflint.hcl"; fi 2>/dev/null' '# Prune plugin links a ruleset bump left dangling, then link each baked plugin version at the path tflint looks for (see the tflint block below).' 'find "$HOME/.tflint.d/plugins" -xtype l -delete 2>/dev/null' 'for p in /opt/tflint/plugins/github.com/terraform-linters/tflint-ruleset-aws/*/tflint-ruleset-aws; do [ -e "$p" ] || continue; d="$HOME/.tflint.d/plugins/${p#/opt/tflint/plugins/}"; [ -e "$d" ] || { mkdir -p "${d%/*}" && ln -s "$p" "$d"; } 2>/dev/null; done' '# tfenv: state lives in the box home (the image is read-only); code, keyring and defaults are root-owned under /opt/tfenv. Every start re-arms signature checks and seeds the default version.' 'if [ -n "${TFENV_CONFIG_DIR:-}" ]; then' '  mkdir -p "$TFENV_CONFIG_DIR/versions" 2>/dev/null' '  rm -f "$TFENV_CONFIG_DIR/use-gnupg" "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null; cp /opt/tfenv/use-gpgv "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null || echo "agent-box: tfenv signature checks NOT armed (cannot write \$TFENV_CONFIG_DIR/use-gpgv)" >&2' '  v=$(cat /opt/tfenv/default-version 2>/dev/null)' '  # Drop the default versions an EARLIER image linked in (versions/X/terraform -> /usr/local/bin/terraform, an unversioned path that now holds a newer binary). tfenv reinstalls such a version, signature-checked, when it is asked for.' '  for d in "$TFENV_CONFIG_DIR"/versions/*; do [ -n "$v" ] && [ "${d##*/}" != "$v" ] && [ "$(readlink "$d/terraform" 2>/dev/null)" = /usr/local/bin/terraform ] && rm -rf "$d" 2>/dev/null; done' '  [ -z "$v" ] || [ -e "$TFENV_CONFIG_DIR/versions/$v/terraform" ] || { mkdir -p "$TFENV_CONFIG_DIR/versions/$v" && ln -s /usr/local/bin/terraform "$TFENV_CONFIG_DIR/versions/$v/terraform"; } 2>/dev/null' '  # Default version rule: .agent-box-default holds the default this script last wrote. If `version` is absent, or byte-equal to that marker (never changed by the user), it follows the image: write the new default into both. Otherwise the user chose a version (tfenv use): leave `version` alone, and still move the marker to the new default.' '  if [ ! -e "$TFENV_CONFIG_DIR/version" ] || cmp -s "$TFENV_CONFIG_DIR/version" "$TFENV_CONFIG_DIR/.agent-box-default"; then cp /opt/tfenv/default-version "$TFENV_CONFIG_DIR/version" 2>/dev/null; fi' '  cp /opt/tfenv/default-version "$TFENV_CONFIG_DIR/.agent-box-default" 2>/dev/null' 'fi' 'exec "$@"' > /usr/local/bin/agent-box-entrypoint && chmod 0755 /usr/local/bin/agent-box-entrypoint

# === frequently bumped pins (keep last) ===
# Everything below moves faster than the heavy layers above, so a version bump
# rebuilds only this tail. Each tool: version ARG + per-arch sha256 ARGs, the
# sum checked with sha256sum -c, the upstream checksum source in a comment, and
# a version check that greps the pinned version. Released versions only, at
# least two weeks old when pinned (2026-10-19). Sums were computed from the
# downloaded artefacts AND compared with the upstream checksums file and/or the
# GitHub asset digest. To bump: change <TOOL>_VERSION, download both arch
# artefacts, recompute both sums, compare them with the same sources.

# --- apt extras ---
# make, dnsutils (dig) and netcat-openbsd (nc) are absent from the slim base;
# openssl is already there (named so the dependency is written down); xz-utils is
# needed to unpack the shellcheck release (.tar.xz). They follow the base's apt
# snapshot (see "base image" above).
RUN apt-get update && apt-get install -y --no-install-recommends \
      make openssl dnsutils netcat-openbsd xz-utils \
    && rm -rf /var/lib/apt/lists/*

# --- kubernetes tools ---
# kubectl 1.37.1 (released 2026-09-23). Sums from the per-binary .sha256 files:
#   https://dl.k8s.io/release/v1.37.1/bin/linux/amd64/kubectl.sha256
#   https://dl.k8s.io/release/v1.37.1/bin/linux/arm64/kubectl.sha256
ARG KUBECTL_VERSION=1.37.1
ARG KUBECTL_SHA256_AMD64=65691ff77eb6fa44c908b77a1082c9f092c3b9733b5cefabec0d1104890e21a8
ARG KUBECTL_SHA256_ARM64=ff749f4b78d9c4f1ec87307df9b50119ed819e2094aa9810cb9acffc3286c8c7
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$KUBECTL_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$KUBECTL_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${a}/kubectl" -o /tmp/kubectl; \
    echo "${sum}  /tmp/kubectl" | sha256sum -c -; \
    install -m 0755 /tmp/kubectl /usr/local/bin/kubectl; rm -f /tmp/kubectl; \
    kubectl version --client | grep -q "v${KUBECTL_VERSION}"

# Helm 4.3.0 (released 2026-09-09). Sums from the release's .sha256sum files:
#   https://get.helm.sh/helm-v4.3.0-linux-amd64.tar.gz.sha256sum
#   https://get.helm.sh/helm-v4.3.0-linux-arm64.tar.gz.sha256sum
ARG HELM_VERSION=4.3.0
ARG HELM_SHA256_AMD64=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb
ARG HELM_SHA256_ARM64=31c5794dd55c66a51e6b7d2e2ac7a114ae8b1de41ff1d9ba51748ac973b06a08
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$HELM_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$HELM_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${a}.tar.gz" -o /tmp/helm.tar.gz; \
    echo "${sum}  /tmp/helm.tar.gz" | sha256sum -c -; \
    mkdir /tmp/helm; tar -C /tmp/helm -xzf /tmp/helm.tar.gz; \
    install -m 0755 "/tmp/helm/linux-${a}/helm" /usr/local/bin/helm; rm -rf /tmp/helm /tmp/helm.tar.gz; \
    helm version | grep -q "v${HELM_VERSION}"

# kubeconform 0.8.0 (released 2026-06-04). Sums from the release's CHECKSUMS file,
# which matches the GitHub asset digests:
#   https://github.com/yannh/kubeconform/releases/download/v0.8.0/CHECKSUMS
ARG KUBECONFORM_VERSION=0.8.0
ARG KUBECONFORM_SHA256_AMD64=9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883
ARG KUBECONFORM_SHA256_ARM64=1f53fc8e81258197a35e8603054162a5af1de8c5af13746c71ab680d9534ed87
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$KUBECONFORM_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$KUBECONFORM_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/yannh/kubeconform/releases/download/v${KUBECONFORM_VERSION}/kubeconform-linux-${a}.tar.gz" -o /tmp/kubeconform.tar.gz; \
    echo "${sum}  /tmp/kubeconform.tar.gz" | sha256sum -c -; \
    mkdir /tmp/kubeconform; tar -C /tmp/kubeconform -xzf /tmp/kubeconform.tar.gz; \
    install -m 0755 /tmp/kubeconform/kubeconform /usr/local/bin/kubeconform; rm -rf /tmp/kubeconform /tmp/kubeconform.tar.gz; \
    kubeconform -v | grep -q "${KUBECONFORM_VERSION}"

# stern 1.34.0 (released 2026-05-02). Sums from the release's checksums.txt, which
# matches the GitHub asset digests:
#   https://github.com/stern/stern/releases/download/v1.34.0/checksums.txt
ARG STERN_VERSION=1.34.0
ARG STERN_SHA256_AMD64=7754adfa653939240f7d20fff4ada9b69cda40c9e70732301f67bb8045f1ef3e
ARG STERN_SHA256_ARM64=e215cfc5e42d71e93b77d3fac8f0df7d736271f44b2d92a5b417eaa588edff3a
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$STERN_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$STERN_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/stern/stern/releases/download/v${STERN_VERSION}/stern_${STERN_VERSION}_linux_${a}.tar.gz" -o /tmp/stern.tar.gz; \
    echo "${sum}  /tmp/stern.tar.gz" | sha256sum -c -; \
    mkdir /tmp/stern; tar -C /tmp/stern -xzf /tmp/stern.tar.gz; \
    install -m 0755 /tmp/stern/stern /usr/local/bin/stern; rm -rf /tmp/stern /tmp/stern.tar.gz; \
    stern --version | grep -q "${STERN_VERSION}"

# --- lint, config and jsonnet tools ---
# yq (mikefarah) 4.54.1 (released 2026-09-29), the raw linux binary. Sums from the
# release's `checksums` file: the SHA-256 column is the 18th hash, per
# `checksums_hashes_order`; it matches the GitHub asset digests:
#   https://github.com/mikefarah/yq/releases/download/v4.54.1/checksums
#   https://github.com/mikefarah/yq/releases/download/v4.54.1/checksums_hashes_order
ARG YQ_VERSION=4.54.1
ARG YQ_SHA256_AMD64=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
ARG YQ_SHA256_ARM64=189088da0c6429ec5178dfaab1a114805f6cab0b61b165ab236efedf1d57a71b
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$YQ_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$YQ_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_${a}" -o /tmp/yq; \
    echo "${sum}  /tmp/yq" | sha256sum -c -; \
    install -m 0755 /tmp/yq /usr/local/bin/yq; rm -f /tmp/yq; \
    yq --version | grep -q "v${YQ_VERSION}"

# ShellCheck 0.11.0 (released 2025-08-04), the static .tar.xz. The release has no
# checksums file, so the sums are the GitHub asset `digest` fields (and match what
# was computed from the download):
#   gh api repos/koalaman/shellcheck/releases/tags/v0.11.0 --jq '.assets[]|.name+" "+.digest'
ARG SHELLCHECK_VERSION=0.11.0
ARG SHELLCHECK_SHA256_AMD64=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198
ARG SHELLCHECK_SHA256_ARM64=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=x86_64; sum="$SHELLCHECK_SHA256_AMD64" ;; \
      arm64) a=aarch64; sum="$SHELLCHECK_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.${a}.tar.xz" -o /tmp/shellcheck.tar.xz; \
    echo "${sum}  /tmp/shellcheck.tar.xz" | sha256sum -c -; \
    mkdir /tmp/shellcheck; tar -C /tmp/shellcheck -xJf /tmp/shellcheck.tar.xz; \
    install -m 0755 "/tmp/shellcheck/shellcheck-v${SHELLCHECK_VERSION}/shellcheck" /usr/local/bin/shellcheck; rm -rf /tmp/shellcheck /tmp/shellcheck.tar.xz; \
    shellcheck --version | grep -q "version: ${SHELLCHECK_VERSION}"

# go-jsonnet 0.22.0 (released 2026-03-24): the release binaries (jsonnet and
# jsonnetfmt), so no Go toolchain is built here. Sums from the release's
# checksums.txt, which matches the GitHub asset digests:
#   https://github.com/google/go-jsonnet/releases/download/v0.22.0/checksums.txt
ARG JSONNET_VERSION=0.22.0
ARG JSONNET_SHA256_AMD64=e87a93ea44e34da92c15636205c7f2240bf3ac92d00ccb855dbb4e7e03ea6941
ARG JSONNET_SHA256_ARM64=3016b049254cb92548ba99304b6f44ec3795e6173f908e3ce3c5add57243fe20
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$JSONNET_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$JSONNET_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/google/go-jsonnet/releases/download/v${JSONNET_VERSION}/go-jsonnet_${JSONNET_VERSION}_linux_${a}.tar.gz" -o /tmp/jsonnet.tar.gz; \
    echo "${sum}  /tmp/jsonnet.tar.gz" | sha256sum -c -; \
    mkdir /tmp/jsonnet; tar -C /tmp/jsonnet -xzf /tmp/jsonnet.tar.gz; \
    install -m 0755 /tmp/jsonnet/jsonnet /tmp/jsonnet/jsonnetfmt /usr/local/bin/; rm -rf /tmp/jsonnet /tmp/jsonnet.tar.gz; \
    jsonnet --version | grep -q "v${JSONNET_VERSION}"; \
    jsonnetfmt --version | grep -q "v${JSONNET_VERSION}"

# --- tflint + its AWS ruleset ---
# tflint 0.64.0 (released 2026-07-17). Sums from the release's checksums.txt, which
# matches the GitHub asset digests:
#   https://github.com/terraform-linters/tflint/releases/download/v0.64.0/checksums.txt
ARG TFLINT_VERSION=0.64.0
ARG TFLINT_SHA256_AMD64=cca9d13e2e1d7a2c627af60ff899a3c9b74212899416aeb96ec764d2ef954537
ARG TFLINT_SHA256_ARM64=560da89aacf59389d4eb029730dd5b109b7288096c32f2726a0d9e783a5ea8eb
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$TFLINT_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$TFLINT_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/terraform-linters/tflint/releases/download/v${TFLINT_VERSION}/tflint_linux_${a}.zip" -o /tmp/tflint.zip; \
    echo "${sum}  /tmp/tflint.zip" | sha256sum -c -; \
    unzip -q /tmp/tflint.zip -d /tmp/tflint; \
    install -m 0755 /tmp/tflint/tflint /usr/local/bin/tflint; rm -rf /tmp/tflint /tmp/tflint.zip; \
    tflint --version | grep -q "TFLint version ${TFLINT_VERSION}"

# tflint-ruleset-aws 0.49.0 (released 2026-09-19), baked in so no box ever runs
# `tflint --init` (which would download a plugin at run time). Sums from the
# release's checksums.txt, which matches the GitHub asset digests:
#   https://github.com/terraform-linters/tflint-ruleset-aws/releases/download/v0.49.0/checksums.txt
# tflint verifies a plugin's signature/attestation only when IT installs the
# plugin; the plugin is installed here instead, so the sha256 pin against the
# release's checksums.txt IS the verification.
# Layout: tflint finds a plugin declared with `source` + `version` at
#   [plugin dir]/[source]/[version]/tflint-ruleset-[name]
# with no "v" on the version (tflint docs, docs/user-guide/plugins.md "Plugin
# directory"; plugin/install.go InstallPath()). The plugin lives read-only at
# /opt/tflint/plugins/... in that layout and the entrypoint links it into
# $HOME/.tflint.d/plugins/ (the home is a bind mount: nothing the image writes
# under /home/agent is visible at run time). The same entrypoint copies
# /etc/agent-box/tflint.hcl to $HOME/.tflint.hcl only when the home has none, so a
# repo's own .tflint.hcl (tflint reads the working directory first) and any
# `tflint --init` it needs keep working. No tflint environment override is set on
# purpose: such a variable would outrank a repo's own config or init path.
# The last two lines check the config parses and the linked plugin loads at the
# pinned version, using a throwaway HOME.
ARG TFLINT_RULESET_AWS_VERSION=0.49.0
ARG TFLINT_RULESET_AWS_SHA256_AMD64=98a530130d33efd49408f3d0ab9198072192ede413cc2a2ce38295d0922eb1c6
ARG TFLINT_RULESET_AWS_SHA256_ARM64=94f4f965a959d1699ae59687ffccc25031f8aaeae6f49756e5d8ca6e36d9f95b
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$TFLINT_RULESET_AWS_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$TFLINT_RULESET_AWS_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/terraform-linters/tflint-ruleset-aws/releases/download/v${TFLINT_RULESET_AWS_VERSION}/tflint-ruleset-aws_linux_${a}.zip" -o /tmp/tflint-ruleset-aws.zip; \
    echo "${sum}  /tmp/tflint-ruleset-aws.zip" | sha256sum -c -; \
    d="/opt/tflint/plugins/github.com/terraform-linters/tflint-ruleset-aws/${TFLINT_RULESET_AWS_VERSION}"; \
    mkdir -p "$d"; unzip -q /tmp/tflint-ruleset-aws.zip -d "$d"; rm -f /tmp/tflint-ruleset-aws.zip; \
    chmod 0755 "$d/tflint-ruleset-aws"; \
    mkdir -p /etc/agent-box; \
    printf 'plugin "aws" {\n  enabled = true\n  version = "%s"\n  source = "github.com/terraform-linters/tflint-ruleset-aws"\n}\n' "${TFLINT_RULESET_AWS_VERSION}" > /etc/agent-box/tflint.hcl; \
    t="$(mktemp -d)"; \
    mkdir -p "$t/.tflint.d/plugins/github.com/terraform-linters/tflint-ruleset-aws/${TFLINT_RULESET_AWS_VERSION}"; \
    ln -s "$d/tflint-ruleset-aws" "$t/.tflint.d/plugins/github.com/terraform-linters/tflint-ruleset-aws/${TFLINT_RULESET_AWS_VERSION}/tflint-ruleset-aws"; \
    HOME="$t" tflint --config /etc/agent-box/tflint.hcl --version | grep -q "ruleset.aws (${TFLINT_RULESET_AWS_VERSION})"; \
    rm -rf "$t"

# --- tfenv ---
# tfenv 3.2.2 (released 2026-04-28), pinned by version AND sha256 (the tarball is
# arch-independent). tfenv publishes no checksums file, so the sum was computed
# from the downloaded tag tarball and cross-checked by diffing the extracted tree
# against `git clone --depth 1 --branch v3.2.2 https://github.com/tfutils/tfenv`
# (identical; tag commit de6ce2e809c155cbc5e2cfeb3b1bef151244e045):
#   https://github.com/tfutils/tfenv/archive/refs/tags/v3.2.2.tar.gz
# To bump: take a release at least two weeks old, recompute the sum, diff the tree
# against the tag, and re-read libexec/tfenv-install (the use-gpgv path below).
#
# Layout. Boxes run with --read-only and an arbitrary --user uid; the only writable
# places are /tmp, the workspace and the box home (a bind mount, so nothing in the
# image layer is ever writable at run time). Code and state are therefore split:
#   TFENV_ROOT=/opt/tfenv      code, share/hashicorp-keys.pgp, default-version and
#     use-gpgv (root-owned, 0755/0644, read-only at run time)
#   TFENV_CONFIG_DIR=/home/agent/.tfenv   state, in the writable box home: versions/<v>,
#     .install-lock-<v>, version, version.prev, and use-gpgv
# The entrypoint seeds that state on EVERY box start: it copies /opt/tfenv/use-gpgv
# in (removing a use-gnupg, which tfenv would check first), creates `version` from
# /opt/tfenv/default-version only if absent (so `tfenv use` survives restarts), and
# links the already-verified /usr/local/bin/terraform in as
# versions/<default>/terraform: no download at build or run time. The default is the
# `version` FILE, never TFENV_TERRAFORM_VERSION, which would override a repo's own
# .terraform-version.
#
# Signature verification is opt-in in tfenv: only if use-gpgv (or use-gnupg) exists
# does `tfenv install` run `gpgv --keyring share/hashicorp-keys.pgp` over
# SHA256SUMS + its .sig (a failure exits non-zero), before sha256 of the zip is
# checked against those verified sums. Without the file tfenv only WARNS and
# installs on sums alone. `trust-tfenv: yes` makes it use the keyring above, which
# is the fingerprint-checked HashiCorp key from the hashicorp-key stage replacing
# tfenv's bundled one. gpgv comes with apt in the base image.
# THREAT MODEL: this protects a download from a tampered mirror or a
# MITM. It does not stop the agent from turning verification off (it owns the home,
# so it can delete use-gpgv or create use-gnupg mid-session, or just fetch any
# binary it likes): the agent has a shell and a network. The entrypoint re-arms the
# check at the next box start; that is accepted. Likewise tfenv reads a repo's
# .terraform-version without sanitising it, so a hostile repo can steer tfenv to
# run a binary from the workspace; that is no worse than the repo's other
# code-execution paths (committed providers, for one).
ARG TFENV_VERSION=3.2.2
ARG TFENV_SHA256=c9c8b2e2588cf026aafa9803dfdefdfc7aa258577c24ea9624fe53f764edea47
RUN set -eu; \
    curl -fsSL "https://github.com/tfutils/tfenv/archive/refs/tags/v${TFENV_VERSION}.tar.gz" -o /tmp/tfenv.tar.gz; \
    echo "${TFENV_SHA256}  /tmp/tfenv.tar.gz" | sha256sum -c -; \
    mkdir /opt/tfenv; tar -C /opt/tfenv --strip-components=1 -xzf /tmp/tfenv.tar.gz; rm -f /tmp/tfenv.tar.gz; \
    chown -R root:root /opt/tfenv; chmod -R go-w,a+rX /opt/tfenv
COPY --from=hashicorp-key /out/hashicorp-keys.pgp /opt/tfenv/share/hashicorp-keys.pgp
ENV TFENV_ROOT=/opt/tfenv
ENV TFENV_CONFIG_DIR=/home/agent/.tfenv
# The shim must come before /usr/local/bin/terraform; this is the effective PATH.
ENV PATH=/opt/tfenv/bin:$PATH
# Root-owned defaults the entrypoint copies into the home. The check runs the REAL
# entrypoint against throwaway dirs, so it proves the seeding and the shim, and
# leaves nothing in the image (GOTMPDIR is unset so it does not create the Go dir).
RUN set -eu; \
    command -v gpgv >/dev/null; \
    chown root:root /opt/tfenv/share/hashicorp-keys.pgp; chmod 0644 /opt/tfenv/share/hashicorp-keys.pgp; \
    printf 'trust-tfenv: yes\n' > /opt/tfenv/use-gpgv; \
    echo "${TERRAFORM_VERSION}" > /opt/tfenv/default-version; \
    chown root:root /opt/tfenv/use-gpgv /opt/tfenv/default-version; \
    chmod 0644 /opt/tfenv/use-gpgv /opt/tfenv/default-version; \
    tfenv --version | grep -q "${TFENV_VERSION}"; \
    mkdir -p /tmp/tfenv-check-home; \
    env -u GOTMPDIR HOME=/tmp/tfenv-check-home TFENV_CONFIG_DIR=/tmp/tfenv-check /usr/local/bin/agent-box-entrypoint true; \
    TFENV_CONFIG_DIR=/tmp/tfenv-check terraform version | grep -q "v${TERRAFORM_VERSION}"; \
    rm -rf /tmp/tfenv-check /tmp/tfenv-check-home

# --- gh (GitHub CLI) ---
# gh 2.102.0 (released 2026-09-30), pinned by version AND per-arch SHA256. Both
# sums were computed from the downloaded tarballs and match the release's
# gh_2.102.0_checksums.txt and the GitHub asset digests:
#   https://github.com/cli/cli/releases/download/v2.102.0/gh_2.102.0_linux_amd64.tar.gz
#   https://github.com/cli/cli/releases/download/v2.102.0/gh_2.102.0_linux_arm64.tar.gz
# To bump: take a stable release at least two weeks old, download both tarballs,
# recompute the sums and compare them with that release's checksums file.
# It lives in /opt/gh and no token is ever set in the image. /usr/local/bin/gh is a
# shim: when a GitHub-bound session mounts the broker's wrapper it runs that
# (which fetches a short-lived token from the broker and runs the real gh);
# otherwise it runs the real gh untouched.
ARG GH_VERSION=2.102.0
ARG GH_SHA256_AMD64=bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386
ARG GH_SHA256_ARM64=7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$GH_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$GH_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${a}.tar.gz" -o /tmp/gh.tar.gz; \
    echo "${sum}  /tmp/gh.tar.gz" | sha256sum -c -; \
    mkdir /opt/gh; tar -C /opt/gh --strip-components=1 -xzf /tmp/gh.tar.gz; rm -f /tmp/gh.tar.gz; \
    /opt/gh/bin/gh --version | grep -q "${GH_VERSION}"; \
    printf '%s\n' '#!/bin/sh' '[ -f /opt/agentbox/agentbox-gh.js ] && exec node /opt/agentbox/agentbox-gh.js "$@"' 'exec /opt/gh/bin/gh "$@"' > /usr/local/bin/gh; \
    chmod 0755 /usr/local/bin/gh

USER agent
ENV HOME=/home/agent
ENV SHELL=/bin/bash
WORKDIR /home/agent
# The browser and the MCP must run as the box's own user, not only as root.
RUN chromium --version && playwright-mcp --version | grep -q "${PLAYWRIGHT_MCP_VERSION}"
ENTRYPOINT ["/usr/local/bin/agent-box-entrypoint"]
CMD ["bash"]
