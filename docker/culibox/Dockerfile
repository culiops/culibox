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
# extra_ca_certs, for the build only: the context always carries extra-ca.pem
# (empty when none is configured). It joins this stage's trust store before the
# first https fetch and is removed again before USER agent, so the shipped image
# holds the stock store. Plain COPY/RUN: the legacy builder must work too.
COPY extra-ca.pem /usr/local/share/ca-certificates/agent-box-extra.crt
RUN f=/usr/local/share/ca-certificates/agent-box-extra.crt; if [ -s "$f" ]; then update-ca-certificates; else rm -f "$f"; fi
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
COPY extra-ca.pem /usr/local/share/ca-certificates/agent-box-extra.crt
RUN f=/usr/local/share/ca-certificates/agent-box-extra.crt; if [ -s "$f" ]; then update-ca-certificates; else rm -f "$f"; fi
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
LABEL org.opencontainers.image.source="https://github.com/culiops/culibox"

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates ripgrep jq less procps curl unzip \
    && rm -rf /var/lib/apt/lists/*
COPY extra-ca.pem /usr/local/share/ca-certificates/agent-box-extra.crt
RUN f=/usr/local/share/ca-certificates/agent-box-extra.crt; if [ -s "$f" ]; then update-ca-certificates; else rm -f "$f"; fi

# --- update checks and telemetry: off, before any tool below runs ---
# The box has no use for self-update (the image is the pin) or for phoning home,
# and several of these write files into /root at build time, which the last root
# RUN refuses. Set before the first RUN that calls terraform, go or claude:
#   DISABLE_UPDATES=1            Claude Code: no self-update (measured 2026-10-19 to
#                                work as a process env, so no settings entry is needed)
#   DO_NOT_TRACK=1               gh (device-id file), mcp-grafana, others
#   GRAFANA_USAGE_STATS=disabled mcp-grafana's usage statistics
#   CHECKPOINT_DISABLE=1         terraform's checkpoint call and its /root/.terraform.d
#   FASTMCP_CHECK_FOR_UPDATES=off mcp-atlassian checks PyPI on every start otherwise
# GOTELEMETRY=off is NOT set: measured to do nothing. The Go RUN runs
# `go telemetry off` first (without it a background process rewrites the counter
# files AFTER an rm -rf, measured 2026-10-19) and then removes /root/.config/go.
ENV DISABLE_UPDATES=1 DO_NOT_TRACK=1 GRAFANA_USAGE_STATS=disabled CHECKPOINT_DISABLE=1 FASTMCP_CHECK_FOR_UPDATES=off

# --- python3 ---
# scripts/lint.sh maps eslint's JSON report back to the source files with
# python3, so a box without it cannot pass scripts/test.sh. Debian bookworm's
# python3 package (no recommends); it moves with the base image's apt snapshot.
# python3-venv is for the mcp-atlassian venv below (bookworm's python3 cannot make
# one without it).
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv \
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
    terraform version; \
    rm -rf /root/.terraform.d

# --- Go ---
# Go 1.26.11, pinned by version AND per-arch SHA256 (from go.dev's release list):
#   https://go.dev/dl/go1.26.11.linux-amd64.tar.gz
#   https://go.dev/dl/go1.26.11.linux-arm64.tar.gz
# To bump: take the new version and sums from
#   https://go.dev/dl/?mode=json&include=all
# and keep GO_VERSION equal to the `go` directive in go.mod (a test enforces it).
ARG GO_VERSION=1.26.11
ARG GO_SHA256_AMD64=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec
ARG GO_SHA256_ARM64=6c2f29b458b5add0835f18b776f69c8881a6d88428f1484a6da55755e620757e
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$GO_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$GO_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${a}.tar.gz" -o /tmp/go.tar.gz; \
    echo "${sum}  /tmp/go.tar.gz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; rm -f /tmp/go.tar.gz; \
    /usr/local/go/bin/go telemetry off; \
    /usr/local/go/bin/go version; \
    rm -rf /root/.config/go; \
    [ ! -e /root/.config ] || rmdir /root/.config
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

# --- C compiler (go test -race) ---
# go test -race needs cgo, and cgo needs a C compiler: without one the race run
# could only happen on the host. From Debian's signed archive, following the
# base's apt snapshot. With gcc on PATH Go turns cgo on by default, which would
# make every Go build in a box (user projects too) a dynamically linked cgo build.
# CGO_ENABLED=0 keeps the default a box had with no compiler; a race run asks for
# cgo itself: CGO_ENABLED=1 go test -race ./...
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
ENV CGO_ENABLED=0

# --- abduco ---
# Built in the abduco-build stage above (see there for source, pin and bump).
COPY --from=abduco-build /out/abduco /usr/local/bin/abduco
RUN abduco -v 2>&1 | grep -q 'abduco-0.6'

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
    && NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION} \
    && npm cache clean --force && rm -rf /root/.npm \
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
# session's MCP bundle (it is read by a probe run with --entrypoint sh).
# codebase-memory-mcp is not shipped (removed 2026-11-06: 4 of 209 box sessions
# since 2026-10-23 called it). A box runs only the MCP servers whose command is in
# this image (the bundle probe drops the rest), so a user's own entry for it works
# in host-tier sessions, not in a box.
# playwright writes its output to /tmp/playwright-mcp (dies with the box, never
# in a clone's working tree) and starts every browser with an in-memory profile
# (--isolated), so no login persists in the box home.
RUN mkdir -p /etc/agent-box && printf '%s\n' '{"mcpServers":{"playwright":{"type":"stdio","command":"/usr/local/bin/playwright-mcp","args":["--headless","--isolated","--output-dir","/tmp/playwright-mcp","--browser","chromium","--executable-path","/usr/bin/chromium"]}}}' > /etc/agent-box/mcp.json && jq -e '.mcpServers["playwright"].command' /etc/agent-box/mcp.json

# Entrypoint: at container start, create $GOTMPDIR, link the tflint default config
# and plugin into the home, and seed and re-arm the tfenv state in the home (see
# the tfenv block), then exec the arguments. The box home is a bind mount of the
# box's state dir and persists across image bumps, so none of this can be made at
# build time, and every step must also repair what an EARLIER image left behind.
# Go refuses a missing GOTMPDIR, hence its mkdir.
# agent-box always passes `sleep infinity`; the probes override --entrypoint.
RUN printf '%s\n' '#!/bin/sh' '# Go refuses a missing GOTMPDIR; the home is a bind mount, so make it here.' '[ -n "${GOTMPDIR:-}" ] && mkdir -p "$GOTMPDIR" 2>/dev/null' '# tflint AWS ruleset: link the default config unless the home holds a REGULAR file (the user-owned one; a repo .tflint.hcl still wins). A symlink, dangling or not, is ours from an earlier start: re-point it, so a ruleset bump is picked up.' 'if [ ! -e "$HOME/.tflint.hcl" ] || [ -L "$HOME/.tflint.hcl" ]; then rm -f "$HOME/.tflint.hcl"; ln -s /etc/agent-box/tflint.hcl "$HOME/.tflint.hcl"; fi 2>/dev/null' '# Prune plugin links a ruleset bump left dangling, then link each baked plugin version at the path tflint looks for (see the tflint block below).' 'find "$HOME/.tflint.d/plugins" -xtype l -delete 2>/dev/null' 'for p in /opt/tflint/plugins/github.com/terraform-linters/tflint-ruleset-aws/*/tflint-ruleset-aws; do [ -e "$p" ] || continue; d="$HOME/.tflint.d/plugins/${p#/opt/tflint/plugins/}"; [ -e "$d" ] || { mkdir -p "${d%/*}" && ln -s "$p" "$d"; } 2>/dev/null; done' '# tfenv: state lives in the box home (the image is read-only); code, keyring and defaults are root-owned under /opt/tfenv. Every start re-arms signature checks and seeds the default version.' 'if [ -n "${TFENV_CONFIG_DIR:-}" ]; then' '  mkdir -p "$TFENV_CONFIG_DIR/versions" 2>/dev/null' '  rm -f "$TFENV_CONFIG_DIR/use-gnupg" "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null; cp /opt/tfenv/use-gpgv "$TFENV_CONFIG_DIR/use-gpgv" 2>/dev/null || echo "agent-box: tfenv signature checks NOT armed (cannot write \$TFENV_CONFIG_DIR/use-gpgv)" >&2' '  v=$(cat /opt/tfenv/default-version 2>/dev/null)' '  # Drop the default versions an EARLIER image linked in (versions/X/terraform -> /usr/local/bin/terraform, an unversioned path that now holds a newer binary). tfenv reinstalls such a version, signature-checked, when it is asked for.' '  for d in "$TFENV_CONFIG_DIR"/versions/*; do [ -n "$v" ] && [ "${d##*/}" != "$v" ] && [ "$(readlink "$d/terraform" 2>/dev/null)" = /usr/local/bin/terraform ] && rm -rf "$d" 2>/dev/null; done' '  [ -z "$v" ] || [ -e "$TFENV_CONFIG_DIR/versions/$v/terraform" ] || { mkdir -p "$TFENV_CONFIG_DIR/versions/$v" && ln -s /usr/local/bin/terraform "$TFENV_CONFIG_DIR/versions/$v/terraform"; } 2>/dev/null' '  # Default version rule: .agent-box-default holds the default this script last wrote. If `version` is absent, or byte-equal to that marker (never changed by the user), it follows the image: write the new default into both. Otherwise the user chose a version (tfenv use): leave `version` alone, and still move the marker to the new default.' '  if [ ! -e "$TFENV_CONFIG_DIR/version" ] || cmp -s "$TFENV_CONFIG_DIR/version" "$TFENV_CONFIG_DIR/.agent-box-default"; then cp /opt/tfenv/default-version "$TFENV_CONFIG_DIR/version" 2>/dev/null; fi' '  cp /opt/tfenv/default-version "$TFENV_CONFIG_DIR/.agent-box-default" 2>/dev/null' 'fi' '# zsh: an existing ~/.zshrc keeps zsh'"'"'s new-user wizard away on a fresh box home; the agent-box defaults load first from /etc/agent-box/zshrc.' '[ -e "$HOME/.zshrc" ] || { echo "# Your zsh settings. agent-box defaults (prompt, completion, plugins) load first from /etc/agent-box/zshrc." > "$HOME/.zshrc"; } 2>/dev/null' 'exec "$@"' > /usr/local/bin/agent-box-entrypoint && chmod 0755 /usr/local/bin/agent-box-entrypoint

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
# needed to unpack the shellcheck release (.tar.xz); python3-pytest runs Python test
# suites (culiops-skills' analytics tests). They follow the base's apt snapshot
# (see "base image" above).
RUN apt-get update && apt-get install -y --no-install-recommends \
      make openssl dnsutils netcat-openbsd xz-utils python3-pytest \
    && rm -rf /var/lib/apt/lists/*

# --- zsh and offline ops tools ---
# zsh with its two plugins, fzf (Ctrl-R history search), and small tools that work
# without a network: ipcalc (CIDR and subnet math), tree, file, bc, and nano as the
# quick-fix editor. All from Debian's signed archive, following the base's apt
# snapshot. fzf's zsh key bindings live under /usr/share/doc, which the slim base
# keeps out of every install; the zz- file (read after the base's own, and dpkg
# applies the last matching rule) lets exactly those files back in.
RUN printf '%s\n' 'path-include=/usr/share/doc/fzf/examples/*.zsh' > /etc/dpkg/dpkg.cfg.d/zz-agent-box-fzf \
    && apt-get update && apt-get install -y --no-install-recommends \
      zsh zsh-autosuggestions zsh-syntax-highlighting fzf ipcalc tree file bc nano \
    && rm -rf /var/lib/apt/lists/* \
    && test -r /usr/share/doc/fzf/examples/key-bindings.zsh \
    && test -r /usr/share/doc/fzf/examples/completion.zsh

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

# --- S3 log analytics tools ---
# The culiops-skills sre-investigate S3-log module shells out to the duckdb and
# s5cmd CLIs; a box cannot fetch GitHub release assets at runtime, so they ship here.
# DuckDB CLI 2.0.0 (released 2026-10-21; the module needs 1.4.0 or later and is
# tested on 2.0.0). The release has no checksums file, so the sums are the GitHub
# asset `digest` fields (and match what was computed from the download):
#   gh api repos/duckdb/duckdb/releases/tags/v2.0.0 --jq '.assets[]|.name+" "+.digest'
ARG DUCKDB_VERSION=2.0.0
ARG DUCKDB_SHA256_AMD64=c6507206baf7793c6013dbd82368469a45f20dc0e17a6f92f204602f652ed2bc
ARG DUCKDB_SHA256_ARM64=859e1875d2a9515fbb4bf8871061437a14af82320da68c753f094c4b776348fe
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=amd64; sum="$DUCKDB_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$DUCKDB_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb-cli-linux-${a}.tar.gz" -o /tmp/duckdb.tar.gz; \
    echo "${sum}  /tmp/duckdb.tar.gz" | sha256sum -c -; \
    mkdir /tmp/duckdb; tar -C /tmp/duckdb -xzf /tmp/duckdb.tar.gz; \
    install -m 0755 /tmp/duckdb/duckdb /usr/local/bin/duckdb; rm -rf /tmp/duckdb /tmp/duckdb.tar.gz; \
    duckdb --version | grep -q "v${DUCKDB_VERSION}"

# s5cmd 2.3.0 (released 2024-12-16; the module needs 2.2.0 or later). Sums from the
# release's s5cmd_checksums.txt:
#   https://github.com/peak/s5cmd/releases/download/v2.3.0/s5cmd_checksums.txt
ARG S5CMD_VERSION=2.3.0
ARG S5CMD_SHA256_AMD64=de0fdbfa3aceae55e069ba81a0fc17b2026567637603734a387b2fca06c299b4
ARG S5CMD_SHA256_ARM64=1439f0d00ecedcd2a2f1f2c6749bbb0152b2257bf5086f29646ec8ae38798e24
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=64bit; sum="$S5CMD_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$S5CMD_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/peak/s5cmd/releases/download/v${S5CMD_VERSION}/s5cmd_${S5CMD_VERSION}_Linux-${a}.tar.gz" -o /tmp/s5cmd.tar.gz; \
    echo "${sum}  /tmp/s5cmd.tar.gz" | sha256sum -c -; \
    mkdir /tmp/s5cmd; tar -C /tmp/s5cmd -xzf /tmp/s5cmd.tar.gz; \
    install -m 0755 /tmp/s5cmd/s5cmd /usr/local/bin/s5cmd; rm -rf /tmp/s5cmd /tmp/s5cmd.tar.gz; \
    s5cmd version | grep -q "v${S5CMD_VERSION}"

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
    rm -rf /tmp/tfenv-check /tmp/tfenv-check-home /root/.terraform.d

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
    rm -rf /root/.local/state/gh; \
    printf '%s\n' '#!/bin/sh' '[ -f /opt/agentbox/agentbox-gh.js ] && exec node /opt/agentbox/agentbox-gh.js "$@"' 'exec /opt/gh/bin/gh "$@"' > /usr/local/bin/gh; \
    chmod 0755 /usr/local/bin/gh

# --- agentbox-env shim ---
# `agentbox-env NAME -- CMD` runs CMD with the named static tokens in its env for
# that one process (a token is never in the session env). The script is mounted
# read-only with the session's binding at /opt/agentbox (the console's BoxDir);
# this shim runs it when it is there. A session with no token binding has no
# script, so the shim says so and exits 127, the same way a missing command does.
RUN printf '%s\n' '#!/bin/sh' '[ -f /opt/agentbox/agentbox-env.js ] && exec node /opt/agentbox/agentbox-env.js "$@"' 'echo "agentbox-env: this session has no token binding" >&2' 'exit 127' > /usr/local/bin/agentbox-env \
    && chmod 0755 /usr/local/bin/agentbox-env

# --- mcp-grafana ---
# mcp-grafana v2.0.0 (released 2026-10-01), the Grafana MCP server, pinned by version
# AND per-arch SHA256. v2.0.0 rather than the newer v2.0.2 (2026-10-07) because of
# the two-week rule: v2.0.2 was 12 days old when this was pinned. Both sums were
# computed from the downloaded tarballs and match the release's checksums.txt and
# the GitHub asset digests:
#   https://github.com/grafana/mcp-grafana/releases/download/v2.0.0/mcp-grafana_2.0.0_checksums.txt
#   https://github.com/grafana/mcp-grafana/releases/download/v2.0.0/mcp-grafana_Linux_x86_64.tar.gz
#   https://github.com/grafana/mcp-grafana/releases/download/v2.0.0/mcp-grafana_Linux_arm64.tar.gz
# The tarball is flat (the binary is at the archive root) and `mcp-grafana --version`
# prints the bare version. It is NOT in /etc/agent-box/mcp.json: it needs a Grafana
# URL and a token, so a session declares it only when it binds a token for it.
# To bump: take a release at least two weeks old, download both tarballs, recompute
# the sums and compare them with that release's checksums file.
ARG GRAFANA_MCP_VERSION=2.0.0
ARG GRAFANA_MCP_SHA256_AMD64=0a0dde2c882c24fedcce79a07d97b232f71730c744b84a09b6b0735c2ca3d024
ARG GRAFANA_MCP_SHA256_ARM64=5b29581cad5ce21a0db67c655e5e7e71a9163b4e1ba1b518559baa3c54133a28
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) a=x86_64; sum="$GRAFANA_MCP_SHA256_AMD64" ;; \
      arm64) a=arm64; sum="$GRAFANA_MCP_SHA256_ARM64" ;; \
      *) echo "unsupported arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/grafana/mcp-grafana/releases/download/v${GRAFANA_MCP_VERSION}/mcp-grafana_Linux_${a}.tar.gz" -o /tmp/mcp-grafana.tar.gz; \
    echo "${sum}  /tmp/mcp-grafana.tar.gz" | sha256sum -c -; \
    mkdir /tmp/mcp-grafana; tar -C /tmp/mcp-grafana -xzf /tmp/mcp-grafana.tar.gz; \
    install -m 0755 /tmp/mcp-grafana/mcp-grafana /usr/local/bin/mcp-grafana; rm -rf /tmp/mcp-grafana /tmp/mcp-grafana.tar.gz; \
    mcp-grafana --version | grep -q "${GRAFANA_MCP_VERSION}"

# --- sentry-mcp ---
# @sentry/mcp-server 0.42.0 (published 2026-09-25) from npm, pinned by version. The
# package's binary is sentry-mcp (`--version` prints `@sentry/mcp-server 0.42.0`);
# it needs Node >= 22.13 (the base has 24) and adds about 136 MB. Not in
# /etc/agent-box/mcp.json, for the same reason as mcp-grafana.
# RESIDUAL, accepted (same class as @playwright/mcp above): the tarball ships no
# npm-shrinkwrap.json or lockfile, so its transitive dependencies (the exact
# @sentry/node 11.0.0-rc.0, and the ranges dotenv ^16.6.1 and zod ^4.4.3) resolve
# at build time and are only as pinned as the npm registry. npm checks each tarball
# against the registry's integrity field, but this build holds no integrity pin of
# its own. The top-level integrity, for a manual check, from `npm view
# @sentry/mcp-server@0.42.0 dist.integrity`:
#   sha512-KmwfBOUMzJdcxeM9Iv6EN4XEjXbGyopTeKizgQr46pW35NTFTRBddVepZytVITS4pnx5pXj6bRHadSowaaRHQQ==
# The npm cache is removed in the same RUN so it never lands in a layer.
# To bump: change SENTRY_MCP_VERSION to a release at least two weeks old, compare
# the new dist.integrity with the tarball's sha512, and re-check that
# `sentry-mcp --version` still resolves.
ARG SENTRY_MCP_VERSION=0.42.0
RUN NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npm install -g @sentry/mcp-server@${SENTRY_MCP_VERSION} \
    && npm cache clean --force && rm -rf /root/.npm \
    && command -v sentry-mcp \
    && sentry-mcp --version | grep -q "${SENTRY_MCP_VERSION}"

# --- mcp-atlassian ---
# mcp-atlassian 0.23.1 (uploaded to PyPI 2026-08-19), the Jira/Confluence MCP
# server, in its own venv (python3-venv, see the python3 block), installed from a
# HASH LOCK with `--require-hashes --only-binary :all: --no-deps`: pip refuses any
# file whose sha256 is not in the lock, and builds nothing from source, so the
# 106 transitive dependencies are pinned like the server itself. The build context
# holds only this Dockerfile, so the lock is inlined below (about 250 lines) and
# removed again in the same RUN. About 193 MB. Not in /etc/agent-box/mcp.json, for
# the same reason as mcp-grafana.
# The lock was generated offline on 2026-10-19 with uv 0.12.23 (not installed in
# this image): `uv pip compile --python-version 3.11 --generate-hashes --no-build`
# on `mcp-atlassian==0.23.1`, once with --python-platform x86_64-manylinux_2_28 and
# once with aarch64-manylinux_2_28 (the two gave the identical 107 pins). uv lists
# every wheel of every pin (about 1,600 lines), so each pin's hashes were then
# trimmed to the wheels this image can install: cp311, abi3 or py3-none-any, on
# manylinux x86_64 or aarch64 up to glibc 2.36 (taken from the PyPI JSON files API,
# intersected with uv's hashes). A platform outside that set fails closed on a
# missing hash. The amd64 install and `pip check` were run for real; the arm64
# wheels were fetched with `pip download --require-hashes` but not executed.
# To bump: change MCP_ATLASSIAN_VERSION to a release at least two weeks old,
# regenerate both platform locks as above, re-trim, replace the printf lines below
# (and the version in the lock's first pin), and re-run both arch installs.
ARG MCP_ATLASSIAN_VERSION=0.23.1
RUN set -eu; \
    printf '%s\n' \
      '# mcp-atlassian 0.23.1 hash lock for the agent-box image (cp311, py3-none-any, manylinux x86_64 + aarch64 wheels only).' \
      '# Generated 2026-10-19: uv 0.12.23 `uv pip compile --python-platform {x86_64,aarch64}-manylinux_2_28 --python-version 3.11' \
      '#   --generate-hashes --no-build` on `mcp-atlassian==0.23.1` (both platforms gave the identical 107 pins), then the hash list' \
      '#   was trimmed to wheels installable on Debian bookworm / Python 3.11 on amd64 or arm64 (PyPI JSON files API, packaging.tags).' \
      '# Install: pip install --no-cache-dir --require-hashes --only-binary :all: --no-deps -r <this file>' \
      'aiofile==3.12.3 \' \
      '    --hash=sha256:5c1bcc9e929c50834608e8cc1a4cc1d7503eb60c15a535b779fd39e2f372c017' \
      'annotated-types==0.8.0 \' \
      '    --hash=sha256:f072f4d804ea359e4eaf198b1af7a8b0943881a87f31bb764f8bf219bb9419e0' \
      'anyio==4.15.1 \' \
      '    --hash=sha256:6152fdbbf9a77fdec97731721bebf7c4c44f7c29b424b0065826173efc7ed101' \
      'async-timeout==5.0.1 \' \
      '    --hash=sha256:39e3809566ff85354557ec2398b55e096c8364bacac9405a7a1fa429e77fe76c' \
      'atlassian-python-api==4.0.7 \' \
      '    --hash=sha256:46a70cb29eaab87c0a1697fccd3e25df1aa477e6aa4fb9ba936a9d46b425933c' \
      'attrs==26.1.0 \' \
      '    --hash=sha256:c647aa4a12dfbad9333ca4e71fe62ddc36f4e63b2d260a37a8b83d2f043ac309' \
      'authlib==1.9.1 \' \
      '    --hash=sha256:8b9be8b1e5174dcbf5520e984f1a22dc2edae5b6e40a61d4cedbf86b97e9e69b' \
      'backports-tarfile==1.2.0 \' \
      '    --hash=sha256:77e284d754527b01fb1e6fa8a1afe577858ebe4e9dad8919e34c862cb399bc34' \
      'beartype==0.23.1 \' \
      '    --hash=sha256:4461b4dc57e3fdd6c8a8464b22cf118002eea107c33e9fef651c69068ab3cce3' \
      'beautifulsoup4==4.15.0 \' \
      '    --hash=sha256:d6f88de62e1d4e38ecb1077eb9724cd0eff29d2a08ca16a401e9b9e93f117cf9' \
      'cachetools==7.2.3 \' \
      '    --hash=sha256:85ef9d2703918bedf7f88caa0b06bb3bb7a71360eb0b05c20796b89fede6e30c' \
      'caio==0.12.9 \' \
      '    --hash=sha256:14483697a27aefd265decb4b595e70814d284f46bc352c75eb9e9dae94a7b398 \' \
      '    --hash=sha256:bf12d4f014b2a33e642ed7905b5787656ede2fc24be21f7c086826ff0d32cec3 \' \
      '    --hash=sha256:e10e3be34fca464cc5e27d3011e284fd0cde0cc488cda4742e765c8e9291647d' \
      'cattrs==26.2.1 \' \
      '    --hash=sha256:a12aaa3453dc8f633a815293179f08b7421ed18d2575c459c3c736f840beac24' \
      'certifi==2026.10.18 \' \
      '    --hash=sha256:73191c47aaa79da394e8fee2dbc2ec827432cab56311122c9e47abf79e390f62' \
      'cffi==2.1.1 \' \
      '    --hash=sha256:3311ed60d36f83378794e1009ac6258bafbf81f7888b4caa7b35a521e3f95813 \' \
      '    --hash=sha256:34e261f78cb6ceaaa36f42f2613f4380d94d9c759a9c73c769ee6e0247364632' \
      'charset-normalizer==3.5.2 \' \
      '    --hash=sha256:1c50fe28bbc2ced33386f298650d91218076c05420e6cbd790b913adc41659e7 \' \
      '    --hash=sha256:211d5a3eb6af8f513b8d4ca19a8c1b7accab1b5f0d3175f9826b03c1a920dc1f \' \
      '    --hash=sha256:b6b751274acb69d77b3323d6b7dbaa3c7fdfc1eb829b7eb61d262f32e1af9685 \' \
      '    --hash=sha256:d19fbd981a488e22cd04883659ca6b08f50b5974f9fd7c95655ef6a043e5893f \' \
      '    --hash=sha256:d760fe2a4d7c3b226cb9026d6a842868d52a7901bd98420e1baf14e80da85cf5' \
      'click==8.5.0 \' \
      '    --hash=sha256:255bc9599cf7748b4b1a446ccc735421bd08a2ae529a8b88597d3de5664ee360' \
      'cryptography==50.0.2 \' \
      '    --hash=sha256:0ddc924c04591c2811ca024d62ecad4f7f6f08af8939c211438f48a16bd23602 \' \
      '    --hash=sha256:1981f1db4630889b9ef7803fadef12b056f428cb6b85c27ba57b774793b6093c \' \
      '    --hash=sha256:4061c0079120205fb760c58acab6443e217307dcf05e3702cf970e0689972856 \' \
      '    --hash=sha256:630ebfea3bf689d075f82316324ff7433dc447fe6bc1bfc76524b74b4a9567d2 \' \
      '    --hash=sha256:79def8d059362e7831389ed3be0ecdf58a89386e1271e35dd9f5af84e81bffd0 \' \
      '    --hash=sha256:87e9ce85beb6b328ba370cc6e6aea483c92617b4c95b1d33a49297eb662bfb04 \' \
      '    --hash=sha256:9dab55f57c74c3cad24c323bacbbd04be4705ba6eb0d92e920b1fc4837ed5079 \' \
      '    --hash=sha256:c5e67125c7dca78d199ec4e116aa93dbb83494808ecbb8211a2cb09b1bf41dbd \' \
      '    --hash=sha256:dfe9763530994147d9af1def057a5b9658b00e8f8fe8743d144d1e0911c2e454 \' \
      '    --hash=sha256:ee247f5c245c9a2fe7c8e2214e295918838e44e00a45a6718451e4004219e767 \' \
      '    --hash=sha256:f21e8a22c8605750c7af886bab299a363721264061b4ac0a30efb73cfd58efc5 \' \
      '    --hash=sha256:f9f6143a8c75945eb960d9eb98905a441394abfa24afaae239d514ffb2586480' \
      'cyclopts==5.2.0 \' \
      '    --hash=sha256:5da2a5d65164e03008621fc4795946b88a722b881398b5dfa58afa6218342cff' \
      'deprecated==1.3.1 \' \
      '    --hash=sha256:597bfef186b6f60181535a29fbe44865ce137a5079f295b479886c82729d5f3f' \
      'dnspython==2.9.0 \' \
      '    --hash=sha256:9a4aedb833c3c1b49214d04d44d3032ab7a9135f7c1d29a549b4ff78fd82fda9' \
      'docstring-parser==0.18.0 \' \
      '    --hash=sha256:b3fcbed555c47d8479be0796ef7e19c2670d428d72e96da63f3a40122860374b' \
      'email-validator==2.3.0 \' \
      '    --hash=sha256:80f13f623413e6b197ae73bb10bf4eb0908faf509ad8362c5edeb0be7fd450b4' \
      'exceptiongroup==1.3.1 \' \
      '    --hash=sha256:a7a39a3bd276781e98394987d3a5701d0c4edffb633bb7a5144577f82c773598' \
      'fakeredis==2.34.1 \' \
      '    --hash=sha256:0107ec99d48913e7eec2a5e3e2403d1bd5f8aa6489d1a634571b975289c48f12' \
      'fastmcp==3.4.9 \' \
      '    --hash=sha256:8ec034ccb896a6b30cef25cbf499d93099482a03acf2c223e7db5531b22e88cb' \
      'fastmcp-slim==3.4.9 \' \
      '    --hash=sha256:f0442024dcaec56860faeb7863099eacbaa921596c88d5e6f9e7f6adc4c1aca4' \
      'griffelib==2.3.2 \' \
      '    --hash=sha256:8e710afededd5607f95bf3c8ccc175ee306e7084459faf27b9f9f44ef2a7a274' \
      'h11==0.16.0 \' \
      '    --hash=sha256:63cf8bbe7522de3bf65932fda1d9c2772064ffb3dae62d55932da54b31cb6c86' \
      'httpcore==1.0.9 \' \
      '    --hash=sha256:2d400746a40668fc9dec9810239072b40b4484b640a8c38fd654a024c7a1bf55' \
      'httpx==0.28.1 \' \
      '    --hash=sha256:d909fcccc110f8c7faf814ca82a9a4d816bc5a6dbfea25d6591d6985b8ba59ad' \
      'httpx-sse==0.4.3 \' \
      '    --hash=sha256:0ac1c9fe3c0afad2e0ebb25a934a59f4c7823b60792691f779fad2c5568830fc' \
      'idna==3.20 \' \
      '    --hash=sha256:ab7ae7122974553370f0bdb919e1a960b2cd1bc1ef0276416d896db81c14582c' \
      'importlib-metadata==9.0.1 \' \
      '    --hash=sha256:bba5600596a7e21f3eef53281cf28d6a5195634d2f2b78ff9501a3272c6eaab0' \
      'jaraco-classes==3.4.0 \' \
      '    --hash=sha256:f662826b6bed8cace05e7ff873ce0f9283b5c924470fe664fff1c2f00f581790' \
      'jaraco-context==6.1.2 \' \
      '    --hash=sha256:bf8150b79a2d5d91ae48629d8b427a8f7ba0e1097dd6202a9059f29a36379535' \
      'jaraco-functools==4.6.0 \' \
      '    --hash=sha256:99e3dc0060c5cbe8fcd1cdb36258e2a65ca40f1566b2033b12abb1bb44dd3c30' \
      'jeepney==0.9.0 \' \
      '    --hash=sha256:97e5714520c16fc0a45695e5365a2e11b81ea79bba796e26f9f1d178cb182683' \
      'jmespath==1.1.0 \' \
      '    --hash=sha256:a5663118de4908c91729bea0acadca56526eb2698e83de10cd116ae0f4e97c64' \
      'joserfc==1.7.7 \' \
      '    --hash=sha256:06979cfbab78bc802f14c0358f32969826cc26720e13cb38926ffdc86bc0cb2c' \
      'jsonref==1.1.0 \' \
      '    --hash=sha256:590dc7773df6c21cbf948b5dac07a72a251db28b0238ceecce0a2abfa8ec30a9' \
      'jsonschema==4.26.0 \' \
      '    --hash=sha256:d489f15263b8d200f8387e64b4c3a75f06629559fb73deb8fdfb525f2dab50ce' \
      'jsonschema-path==0.5.0 \' \
      '    --hash=sha256:2790a070bc7abb08ea3dbe4d340ece4efadf639223001f020c7503229ba068e2' \
      'jsonschema-specifications==2025.9.1 \' \
      '    --hash=sha256:98802fee3a11ee76ecaca44429fda8a41bff98b00a0f2838151b113f210cc6fe' \
      'keyring==25.7.0 \' \
      '    --hash=sha256:be4a0b195f149690c166e850609a477c532ddbfbaed96a404d4e43f8d5e2689f' \
      'lxml==6.1.3 \' \
      '    --hash=sha256:49fbc2682a9306135b7ec49e93f97f9c26689b9b7f96ed2742d8d6497e994d13 \' \
      '    --hash=sha256:4a579dfb9c835f8ab47f4b8ed33440cbc75b806b73297208e6ec2a33e903740b \' \
      '    --hash=sha256:527195c188d7d0af748cd48d220ab8cdc5cb99be3d49ac4d9be7324d8abf9bc0 \' \
      '    --hash=sha256:ea2c01cdb16dc12156e455007c406dfaaece0c89aa4ba0e3b47586779f951d41' \
      'markdown==3.11.1 \' \
      '    --hash=sha256:f1fa378ba5d682900c9ecb55ccceacca936016dda7c3b27097e8ae03ff78feb5' \
      'markdown-it-py==4.2.0 \' \
      '    --hash=sha256:9f7ebbcd14fe59494226453aed97c1070d83f8d24b6fc3a3bcf9a38092641c4a' \
      'markdown-to-confluence==0.6.4 \' \
      '    --hash=sha256:3da8f89d50f70d0efa18b963f536ce1a8ac190f424d382e0da329b8e4c27e8d2' \
      'markdownify==1.2.3 \' \
      '    --hash=sha256:a189a0bedfd14009030fde5f85bb6f77c56897cb839b5c25315dd7d4e3e290ba' \
      'mcp==1.30.0 \' \
      '    --hash=sha256:666edb5009503e1047c9d60346a756f94b261f05cc2625f23d41c728ffc484d0' \
      'mcp-atlassian==0.23.1 \' \
      '    --hash=sha256:2c00820dabb6d39d9356025a52689cbc411ec4f7d7dd87a64589099084da471a' \
      'mdurl==0.1.2 \' \
      '    --hash=sha256:84008a41e51615a49fc9966191ff91509e3c40b939176e643fd50a5c2196b8f8' \
      'more-itertools==11.2.1 \' \
      '    --hash=sha256:35a7377edd1dd6608dcb2cdf534ded55ea32d49448875ad042cd3f879fb1ded0' \
      'oauthlib==4.0.0 \' \
      '    --hash=sha256:624c28c13a0a59cabf9747dfa52af63be3e512a7f2714df16e91b5b3a145e6cd' \
      'openapi-pydantic==0.6.0 \' \
      '    --hash=sha256:8bd6d548a250da37db96f461bcd52b3468e8834ad7d6db14b00a6b2ebbd163a3' \
      'opentelemetry-api==1.45.1 \' \
      '    --hash=sha256:b31553efa588ae44bc306f863c785c5333a9ecc091248c6ee68b4b6c87fdedfb' \
      'orjson==3.13.0 \' \
      '    --hash=sha256:89bcf2d4bc6c9a7e1763c8cf534f38712e66b76a0fefda7fb7785462f0d635e4 \' \
      '    --hash=sha256:dd9d9a101bd8dbfad112170f009cd155e52bb8c936468821a0d03cbb96c0e426' \
      'outcome==1.3.0.post0 \' \
      '    --hash=sha256:e771c5ce06d1415e356078d3bdd68523f284b4ce5419828922b6871e65eda82b' \
      'packaging==26.3 \' \
      '    --hash=sha256:d7193f7c8e4e93f444fde0262bf90af30e16fa0ad0ad44cb553c87339b23cd1c' \
      'pathable==0.6.0 \' \
      '    --hash=sha256:82c4ca6c98c502ad12e0d4e9779b6210afee93c38990988c8c5d1b49bdcdf566' \
      'pathspec==1.1.1 \' \
      '    --hash=sha256:a00ce642f577bf7f473932318056212bc4f8bfdf53128c78bbd5af0b9b20b189' \
      'platformdirs==4.13.5 \' \
      '    --hash=sha256:53d3ec4280b815c1e0b02edad7f9ee8fda1ba9c0865dd9c2e6e63849ce82fc1e' \
      'py-key-value-aio==0.4.6 \' \
      '    --hash=sha256:820b30c7959fc738ea3d00160cdb10cd258f51e38737b2add530defeea1ded77' \
      'pycparser==3.11 \' \
      '    --hash=sha256:51d5a8ba2be0bbe440b99d2112604c95bbbc3c2748a64260186c541e1729cd80' \
      'pydantic==2.14.1 \' \
      '    --hash=sha256:9195d967ec791692a04438115466764fb8b9a27b31f14a760437694f40d6b454' \
      'pydantic-core==2.50.1 \' \
      '    --hash=sha256:8812592c85d0edf423f10eadcef42716d71e8219085ad9e85b775057b7306133 \' \
      '    --hash=sha256:94be440c03fede26969a5ce75468e0e6a9927a1b46d9b679ee8adc1b057b0350' \
      'pydantic-settings==2.16.0 \' \
      '    --hash=sha256:7e73acf7f61936a15e5a3b6eedaea29f133357faf7272f2607ba479b049dd7f2' \
      'pygments==2.21.0 \' \
      '    --hash=sha256:2363c69b61c4a97c838da3b130dcd6468f4848992b21a82f2a63ec34377137d9' \
      'pyjwt==2.15.1 \' \
      '    --hash=sha256:42d59d631f7768a1028a64c7ff581a9bf7519804daf91fc5b6c56e30eec5e193' \
      'pymdown-extensions==12.3 \' \
      '    --hash=sha256:345493a8e3497e24382902903a1c43a28e34a52f2b7571a1c9641429b820ca74' \
      'pyperclip==1.11.0 \' \
      '    --hash=sha256:299403e9ff44581cb9ba2ffeed69c7aa96a008622ad0c46cb575ca75b5b84273' \
      'pysocks==1.7.1 \' \
      '    --hash=sha256:2725bd0a9925919b9b51739eea5f9e2bae91e83288108a9ad338b2e3a4435ee5' \
      'python-dateutil==2.9.0.post0 \' \
      '    --hash=sha256:a8b2bc7bffae282281c8140a97d3aa9c14da0b136dfe83f850eea9a5f7470427' \
      'python-dotenv==1.2.4 \' \
      '    --hash=sha256:42269a8a5b3fd54ffa6f3d84b18abed50064717576b4ecf03dc4a55d8aa04fdc' \
      'python-multipart==0.0.32 \' \
      '    --hash=sha256:ff6d3f776f16878c894e52e107296ffc890e913c611b1a4ec6c44e2821fe2e23' \
      'pyyaml==6.0.3 \' \
      '    --hash=sha256:10892704fc220243f5305762e276552a0395f7beb4dbf9b14ec8fd43b57f126c \' \
      '    --hash=sha256:b8bb0864c5a28024fac8a632c443c87c5aa6f215c0b126c449ae1a150412f31d' \
      'rapidfuzz==3.14.6 \' \
      '    --hash=sha256:3781cf14f9fc933d7198c2b25a8bbbd1a62b752746d5cd26de14957edc0e802f \' \
      '    --hash=sha256:3d5d90bae3c6fb7ea34da968c9f23070e8440edb827a28b242580e0108110b14' \
      'redis==8.1.0 \' \
      '    --hash=sha256:a4fe1aac3d3b3cc791d4b3d5931c5a956045dc951ee74d1c913ee3ac4d2ee9fb' \
      'referencing==0.37.0 \' \
      '    --hash=sha256:381329a9f99628c9069361716891d34ad94af76e461dcb0335825aecc7692231' \
      'requests==2.34.2 \' \
      '    --hash=sha256:2a0d60c172f83ac6ab31e4554906c0f3b3588d37b5cb939b1c061f4907e278e0' \
      'requests-oauthlib==2.0.0 \' \
      '    --hash=sha256:7dd8a5c40426b779b0868c404bdef9768deccf22749cde15852df527e6269b36' \
      'rich==15.0.0 \' \
      '    --hash=sha256:33bd4ef74232fb73fe9279a257718407f169c09b78a87ad3d296f548e27de0bb' \
      'rich-rst==2.2.0 \' \
      '    --hash=sha256:1ea1c43813dc4a8d86475fce27ffb74e0ada3d4656c9f8130ba2a59868055fe9' \
      'rpds-py==2026.9.1 \' \
      '    --hash=sha256:136a1c3fe4402b7008bc81cb62ee538481795b61a7e83df88dff3b3f02b726ff \' \
      '    --hash=sha256:e01b3c878c8641913e688edd1b3f08658c6783d29cf6b826bd3c0d1ae7a1ffaa' \
      'secretstorage==3.5.0 \' \
      '    --hash=sha256:0ce65888c0725fcb2c5bc0fdb8e5438eece02c523557ea40ce0703c266248137' \
      'six==1.17.0 \' \
      '    --hash=sha256:4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274' \
      'sniffio==1.3.1 \' \
      '    --hash=sha256:2f6da418d1f1e0fddd844478f41680e794e6051915791a034ff65e5f100525a2' \
      'sortedcontainers==2.4.0 \' \
      '    --hash=sha256:a163dcaede0f1c021485e957a39245190e74249897e2ae4b2aa38595db237ee0' \
      'soupsieve==2.10 \' \
      '    --hash=sha256:8596eb8967d744174820280fa62b4542a2e955bfaccca73ed8a13c6eb8e9b502' \
      'sse-starlette==3.5.0 \' \
      '    --hash=sha256:3e6e1070df3f0f5d9cea81496de92dbb72f6721871d99748ece67441dd8b7997' \
      'starlette==1.9.0 \' \
      '    --hash=sha256:fd3f159936e882d06c1108f84688f7cda2cf85fc6a592a50edbd175e882c2ddc' \
      'thefuzz==0.22.1 \' \
      '    --hash=sha256:59729b33556850b90e1093c4cf9e618af6f2e4c985df193fdf3c5b5cf02ca481' \
      'trio==0.34.0 \' \
      '    --hash=sha256:6c7c9f49917694dcdcd5f67abd168df5599eca480d61f29854d17a61a75c2f05' \
      'truststore==0.10.5 \' \
      '    --hash=sha256:9aaaedaefaf06d8b206278cf8b5012bc897f485a874503501e12d776df78951c' \
      'types-cachetools==7.0.0.20260713 \' \
      '    --hash=sha256:6db9bcc7a3840d39e91c04117d85a9d0937eacc9d14d12a873e2b01a2d24a71d' \
      'types-python-dateutil==2.9.0.20260807 \' \
      '    --hash=sha256:54aa3707350ed7a9cc0776fd2f6739679d6967d11b40150985e81edcb86df4db' \
      'typing-extensions==4.16.0 \' \
      '    --hash=sha256:481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8' \
      'typing-inspection==0.4.4 \' \
      '    --hash=sha256:65b8397ba37ccbce054456aaccddfc91e6e3083c92824df348d96ca832f3f147' \
      'uncalled-for==0.4.1 \' \
      '    --hash=sha256:b611b38879d9fe5088297e7bb28f2ddd12d2d73dad933d93da107fb63eb4058e' \
      'unidecode==1.4.0 \' \
      '    --hash=sha256:c3c7606c27503ad8d501270406e345ddb480a7b5f38827eafe4fa82a137f0021' \
      'urllib3==2.8.0 \' \
      '    --hash=sha256:0cf3cae568d36aa9576b28dfb35f11328f1cb974ca7647d9475ebb86c75ac6e3' \
      'uvicorn==0.54.0 \' \
      '    --hash=sha256:505bdb0f318731d45f1f712071fc781a8981f6847a31c902c9f5e652d4f67faf' \
      'watchfiles==1.3.0 \' \
      '    --hash=sha256:4af9c464d410e8c44b58ddf1cbec7c3a02c1cb2c6c80df9c74659ba2fbda0ab5 \' \
      '    --hash=sha256:b5768b49e426fd5b550b012c866db347cdf15c398ef98dc557b6e6b72fa74cd1' \
      'websockets==17.2 \' \
      '    --hash=sha256:376a693697ddb695ea282ead76060f4847f90e564b12b4389f2c7589e6fadb9e \' \
      '    --hash=sha256:6aa59f0ef92e796b2db6f5f26550c4713c0e4036899fadf02f55e2ed4db0b7ae \' \
      '    --hash=sha256:ecd63d0c7ed0d3d719c91b5a3861f0f0b3cec9bf223033ddf69d17aaac74bb6d' \
      'wrapt==2.5.1 \' \
      '    --hash=sha256:c25c594f58ecb676358d6d6b0ff068b8bbbc506dc831c6d17876460c66ce39c2 \' \
      '    --hash=sha256:c6e6c226b1ca5402d7ae5fb34a0d21f1b49124fe4200e5884d1e19e53c47ac1d \' \
      '    --hash=sha256:e85a9db9e5a5ccc326edb19e35a5106ba16e451d570a2ec8ea9deb1ea52a3c42' \
      'zipp==4.1.1 \' \
      '    --hash=sha256:8979f52d874162f485ff2981e3891f3a3317b7a3dd43ff1e1775b9304f307a9c' > /tmp/req.txt; \
    python3 -m venv /opt/mcp-atlassian && PIP_CERT=/etc/ssl/certs/ca-certificates.crt /opt/mcp-atlassian/bin/pip install --no-cache-dir --require-hashes --only-binary :all: --no-deps -r /tmp/req.txt; \
    rm -f /tmp/req.txt; rm -rf /root/.cache/pip; \
    ln -s /opt/mcp-atlassian/bin/mcp-atlassian /usr/local/bin/mcp-atlassian; \
    mcp-atlassian --version | grep -q "${MCP_ATLASSIAN_VERSION}"

# --- zsh defaults ---
# The box's zshrc (prompt, history, completion, plugins), loaded by every
# interactive zsh before ~/.zshrc, so the user's own file wins. Pinned by sha256,
# so an edit to it changes this Dockerfile, and with it the image label that
# tells doctor to rebuild. Completions for the tools above are generated here
# (HOME pointed away so nothing lands in /root).
ARG AGENT_BOX_ZSHRC_SHA256=a59e93d3f4e2fa674e8428ce88885451edf99383a42a4bcfd18f4d4563b89be2
COPY zshrc /etc/agent-box/zshrc
RUN set -eu; \
    echo "${AGENT_BOX_ZSHRC_SHA256}  /etc/agent-box/zshrc" | sha256sum -c -; \
    chmod 0644 /etc/agent-box/zshrc; \
    printf '\n%s\n' '# agent-box defaults; ~/.zshrc loads after this and wins.' '[[ -r /etc/agent-box/zshrc ]] && source /etc/agent-box/zshrc' >> /etc/zsh/zshrc; \
    printf '\n%s\n' '# agent-box runs compinit itself (/etc/agent-box/zshrc, cache in ~/.cache/zsh); skip Debian'"'"'s second one.' 'skip_global_compinit=1' >> /etc/zsh/zshenv; \
    d=/usr/local/share/zsh/site-functions; mkdir -p "$d"; \
    export HOME=/tmp/zsh-compgen; mkdir -p "$HOME"; \
    kubectl completion zsh > "$d/_kubectl"; \
    helm completion zsh > "$d/_helm"; \
    stern --completion zsh > "$d/_stern"; \
    yq shell-completion zsh > "$d/_yq"; \
    /opt/gh/bin/gh completion -s zsh > "$d/_gh"; \
    rm -rf /tmp/zsh-compgen; \
    zsh -n /etc/agent-box/zshrc

# --- network diagnostics: a stand-in, not the tool ---
# On Linux a box has no direct network: it reaches the internet only through
# agent-box's HTTPS proxy, by hostname. A ping or trace from inside would measure
# that fence, not the target, and an agent would read the loss as an outage. So
# these tools are not installed; each name says why and what works instead, and
# exits 127 like a missing command.
RUN printf '%s\n' '#!/bin/sh' \
      'cat >&2 <<EOF' \
      'agent-box: ${0##*/} is not in the box image. On Linux a box reaches the internet only' \
      'through agent-box'"'"'s HTTPS proxy (port 443, by hostname), so a ping or trace from here' \
      'would measure that fence, not the target. Diagnose the network where it is: on the' \
      'target (e.g. aws ssm start-session) or from a host-tier shell profile. To check HTTPS' \
      'reachability from the box:' \
      "  curl https://<host>/ -sS -o /dev/null -w '%{http_code} %{time_total}s\\\\n'" \
      'EOF' \
      'exit 127' > /usr/local/bin/agent-box-no-network \
    && chmod 0755 /usr/local/bin/agent-box-no-network \
    && for n in ping mtr traceroute tracepath httping; do ln -s agent-box-no-network "/usr/local/bin/$n"; done

# --- Claude Code ---
# @anthropic-ai/claude-code, pinned to the version the npm `stable` dist-tag points
# at (measured 2026-11-06: stable 2.1.311, latest 2.1.318). The `stable` tag is the
# deliberate exception to the two-week rule: Anthropic's own soak period stands in
# for ours, and it is a deliberate downgrade from `latest`. Self-update is off
# (DISABLE_UPDATES above), so the image is the only way this version moves.
# RESIDUAL, accepted (same class as @playwright/mcp): the package's dependency
# tree resolves at build time. The npm cache is removed in the same RUN so it
# never lands in a layer.
# To bump: re-read `npm view @anthropic-ai/claude-code dist-tags`, change
# CLAUDE_CODE_VERSION to `stable`, rebuild.
# Last of the pins on purpose: it is bumped most often (npm stable moves about
# weekly), and every layer after a changed one is rebuilt and re-pulled. Here a
# bump costs its own layer only; first in the tail it rebuilt ~1.2 GB.
ARG CLAUDE_CODE_VERSION=2.1.311
RUN NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && npm cache clean --force && rm -rf /root/.npm && claude --version | grep -q "${CLAUDE_CODE_VERSION}"

# Claude Code adds a "Co-Authored-By: Claude" trailer (and a "Generated with" line
# in PR text) to what it commits unless told not to. A box commits as the agent,
# clone boxes run with core.hooksPath=/dev/null so no repo commit-msg hook strips
# it, and the config bundle reaches only some sessions, so the image pins it off as
# Claude Code's managed policy: root-owned, read by every claude in every box, and
# above any user, project or --settings value. Both spellings: Claude Code 2.1.311
# reads attribution (an empty commit/pr text hides it; sessionUrl false drops the
# Claude-Session trailer of Remote Control sessions) and the deprecated
# includeCoAuthoredBy. Small, so it sits after the big layer and costs a bump nothing.
RUN mkdir -p /etc/claude-code && printf '%s\n' '{"includeCoAuthoredBy":false,"attribution":{"commit":"","pr":"","sessionUrl":false}}' > /etc/claude-code/managed-settings.json && jq -e '.includeCoAuthoredBy == false and .attribution.commit == "" and .attribution.pr == "" and .attribution.sessionUrl == false' /etc/claude-code/managed-settings.json

# Nothing but the base image's two dotfiles may be left in /root: every RUN above
# cleans after itself in its own layer (a later layer cannot remove an earlier
# one's bytes), and this fails the build on anything that slipped through.
# /root/.ssh is an empty directory some layer leaves behind; rmdir fails the build
# if it ever holds something.
RUN set -eu; \
    [ ! -e /root/.ssh ] || rmdir /root/.ssh; \
    find /root -mindepth 1 | sort > /tmp/rootls; \
    printf '%s\n' /root/.bashrc /root/.profile | sort > /tmp/rootok; \
    diff /tmp/rootok /tmp/rootls; \
    rm /tmp/rootls /tmp/rootok

# The build-time CAs end here: the stock trust store is what ships. Never ENV
# a CA variable above; the console sets them on the box at run time.
RUN rm -f /usr/local/share/ca-certificates/agent-box-extra.crt && update-ca-certificates --fresh

USER agent
ENV HOME=/home/agent
ENV SHELL=/bin/bash
WORKDIR /home/agent
# The browser and the MCP must run as the box's own user, not only as root.
RUN chromium --version && playwright-mcp --version | grep -q "${PLAYWRIGHT_MCP_VERSION}"
ENTRYPOINT ["/usr/local/bin/agent-box-entrypoint"]
CMD ["bash"]
