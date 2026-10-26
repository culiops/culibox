# Running in Docker

Sessions are made restart-proof by [abduco](https://www.brain-dump.org/projects/abduco/):
each agent runs under an abduco server that outlives the daemon, so restarting the
`agent-box` process (or its container) re-attaches to still-running agents
instead of killing them.

For this to actually survive a container restart, two things must hold.

## 1. The image must bundle `abduco`

abduco is a hard dependency: the daemon refuses to start without it. It is a tiny
C program (no runtime deps). Either install a packaged version or build v0.6 from
source in the image:

```dockerfile
# Build abduco from source (not always in distro repos)
RUN apk add --no-cache build-base ncurses-dev \
 && wget -O /tmp/abduco.tar.gz https://www.brain-dump.org/projects/abduco/abduco-0.6.tar.gz \
 && tar -C /tmp -xf /tmp/abduco.tar.gz \
 && make -C /tmp/abduco-0.6 && make -C /tmp/abduco-0.6 install \
 && rm -rf /tmp/abduco*
```

(Or set `abduco_bin` in the config to a path you provide.)

## 2. The socket dir and data dir must be on a persistent volume

abduco's control sockets live under `abduco_socket_dir` (default
`<data_dir>/abduco`), and session metadata lives at `<data_dir>/sessions.json`
with pidfiles under `<data_dir>/sessions/pids`. If `data_dir` is inside the
container's ephemeral layer, a container replacement wipes the sockets and the
surviving agents become unreachable, and persistence is defeated.

Point the data root at a named volume (or host bind mount). Everything Culibox
writes lives there, its `config.json` included, so nothing lands on the container's
ephemeral filesystem:

```yaml
services:
  agent-box:
    image: agent-box
    environment:
      AGENT_BOX_HOME: /data   # config.json goes in /data; leave data_dir out of it
    volumes:
      - agent-data:/data
volumes:
  agent-data:
```

## Caveats

- **Restarting the container process** (same container, e.g. `docker restart`)
  survives cleanly: the abduco servers are child processes of the container's PID 1
  namespace and keep running only if PID 1 stays up. A full container **stop**
  tears down every process including the abduco servers, so persistence covers the
  *daemon* restarting, not the whole container being destroyed. For agents to
  survive the container itself stopping, run abduco servers under an init/PID-1 that
  keeps them alive, or keep the container up and restart only the daemon inside it.
- No cross-restart scrollback: on re-attach the terminal repaints (best-effort);
  a line-mode program's pre-restart output is not replayed.
