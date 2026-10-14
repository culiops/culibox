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
URL containing a one-time-generated token (`#token=…`). Open it in a browser on
the same Mac. The token lives in `~/.local/share/agent-box/login.token`.

Closing the terminal tab stops agent-box but not your sessions; run
`agent-box serve` again and they reattach. Only one agent-box can serve a data
directory at a time.

## Configure

Optional: `~/.config/agent-box/config.json` (see `config.example.json`). To reach
agent-box through a reverse proxy, add its hostname to `allowed_hosts`; any other
Host header is refused.

## Rotate credentials

- New login token: delete `login.token` and restart.
- Log out every browser: delete `cookie.secret` and restart.
