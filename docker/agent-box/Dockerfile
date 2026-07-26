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

# The workspace is bind-mounted from the host at its identical path, so files
# must be owned by the host uid/gid or the agent cannot write to its own repo.
# `docker run --user` overrides these at run time; the build args keep the image
# usable standalone and make the intent explicit.
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
