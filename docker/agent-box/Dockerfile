# The agent box: the filesystem an agent session is allowed to see.
#
# What is NOT here is the point. No host home, no ~/.ssh, no docker socket, no
# inherited daemon environment — the console mounts exactly the project
# directory and a per-box state volume, and forwards environment by name.
FROM node:24-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates ripgrep jq less procps curl unzip \
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
# Terraform is verified against HashiCorp's SHA256SUMS fetched from the same
# origin: it catches a corrupt download, not a compromised origin.
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
    case "$(dpkg --print-architecture)" in amd64) a=amd64 ;; arm64) a=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac; \
    cd /tmp; \
    curl -fsSLO "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${a}.zip"; \
    curl -fsSLO "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_SHA256SUMS"; \
    grep " terraform_${TERRAFORM_VERSION}_linux_${a}.zip\$" "terraform_${TERRAFORM_VERSION}_SHA256SUMS" | sha256sum -c -; \
    unzip -q "terraform_${TERRAFORM_VERSION}_linux_${a}.zip" -d /usr/local/bin; rm -f terraform_*; \
    terraform version

RUN npm install -g @anthropic-ai/claude-code

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

USER agent
ENV HOME=/home/agent
ENV SHELL=/bin/bash
WORKDIR /home/agent
