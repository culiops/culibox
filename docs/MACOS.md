# Running agent-box on macOS

Host tier only for now: sessions run as your user, **not contained**. The boxed
tier (Docker/colima) is not supported on macOS yet.

## Install (from source)

    brew install go abduco
    tar xzf agent-box-src-<sha>.tar.gz && cd agent-box-src-<sha>
    go build -ldflags "-X main.commit=<sha>" -o agent-box ./cmd/agent-box
    go test ./internal/procinfo/... ./internal/abduco/... ./internal/session/... ./internal/config/...

A binary you build yourself is not quarantined by Gatekeeper.

## Check the machine

    ./agent-box doctor

Every line is PASS, WARN or FAIL. The report contains no secrets, so it is safe
to paste into an issue.

## Run

    ./agent-box serve

With no config file, agent-box listens on `127.0.0.1:8899` and prints a login
URL containing a login token (`#token=…`), generated on first start and kept in
`~/.local/share/agent-box/login.token`. Open the URL in a browser on the same Mac.

Closing the terminal tab stops agent-box but not your sessions; run
`agent-box serve` again and they reattach. Only one agent-box can serve a data
directory at a time.

## Configure

Everything agent-box writes lives in one directory, `~/.local/share/agent-box`
(set `AGENT_BOX_HOME` to an absolute path to move it): its config, login token,
session records, profiles and analytics. Back it up, move it or delete it as a
unit. Claude Code's own files stay in `~/.claude`; agent-box only reads them. The one
exception is the abduco socket directory, `/tmp/agent-box-<uid>-<hash>`, because macOS
caps a socket path at 103 bytes; it holds only live sockets.

Optional config: `~/.local/share/agent-box/config.json` (see `config.example.json`), or
pass `-config <path>`. When `AGENT_BOX_HOME` is unset, `~/.config/agent-box/config.json`
(or `$XDG_CONFIG_HOME/agent-box/config.json`) is still read as a fallback. A
`config.json` in the current directory is deliberately not read, so running agent-box
inside a cloned repo never loads that repo's config. To reach
agent-box through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

Both files sit in the data directory (default `~/.local/share/agent-box`).

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.
