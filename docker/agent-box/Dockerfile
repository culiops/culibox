# The agent box: the filesystem an agent session is allowed to see.
#
# What is NOT here is the point. No host home, no ~/.ssh, no docker socket, no
# inherited daemon environment — the console mounts exactly the project
# directory and a per-box state volume, and forwards environment by name.
FROM node:24-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git ca-certificates ripgrep jq less procps \
    && rm -rf /var/lib/apt/lists/*

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
