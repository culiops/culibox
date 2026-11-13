# Static tokens for agents

Some services have no short-lived credential to mint: Grafana service-account tokens, Sentry auth tokens, Atlassian API tokens, a GitHub personal access token. A boxed profile can bind **static token entries** that you define once in `config.json`. The agent gets a token only for one command at a time, through a small in-box wrapper, and the box's egress is opened to exactly the hosts the entry names. A bound spawn is refused on the host tier. On macOS it goes through the egress allowlist like on Linux (the allowlist is verified on a Mac; the token wrapper there is not yet; see [docs/MACOS.md](MACOS.md#every-box)).

## What this does and does not protect against

- **The agent can read a bound token.** It can run `agentbox-env grafana-prod -- env`, read the child's environment, or call the broker socket itself.
- What it prevents is accidental exposure: the token is not inherited by every process and MCP server in the box, does not show up in `env` dumps and is not pasted into transcripts. You also get an audit row per fetch, an egress lock and scoping to the hosts you list.
- It does not stop a deliberate read, and the token outlives the session. The UI says so next to the picker: *"The agent can read this token. It is long-lived and is not revoked when the session ends."*
- **Egress is per session, not per process.** Once an entry is bound, every process in the box can reach that entry's hosts, not only the `agentbox-env` child.

## Set up

**1. Define the entry.** Put the token in a file owned by the user that runs CuliBox, a regular file (not a symlink) with mode `0600` or tighter, or in the daemon's own environment. Then declare the entry in `config.json` and restart CuliBox:

```json
"static_tokens": [
  { "name": "grafana-prod", "provider": "grafana", "label": "Grafana", "prod": true,
    "value": { "type": "file", "path": "/etc/agent-box/tokens/grafana-prod" },
    "env": ["GRAFANA_SERVICE_ACCOUNT_TOKEN"],
    "config_env": { "GRAFANA_URL": "https://grafana.example.com" },
    "egress": [ { "host": "grafana.example.com" } ],
    "mcp": { "server": "grafana-prod", "command": "mcp-grafana" } }
]
```

| Field | Rules |
|---|---|
| `name` | Required, unique. `^[a-z0-9][a-z0-9-]{0,62}$`. Profiles refer to the entry by it. |
| `value` | Required. Where the token lives: `{"type": "file", "path": "<absolute path>"}` (0600, owned by the user that runs CuliBox, not a symlink) or `{"type": "env", "var": "<NAME>"}` (the daemon's own environment; a name starting with `AB_`, or the `password_env` variable, is refused). Any other type is refused with `unsupported reference type "..." (want "file" or "env")`. One trailing newline is trimmed; an empty value or one with a control character is refused. The reference is read at each fetch and never cached. |
| `env` | Required, 1 to 4 variable names (`^[A-Za-z_][A-Za-z0-9_]*$`) that carry the token. Names that change how programs run or connect (`PATH`, `HOME`, `NODE_OPTIONS`, `LD_*`, `*_proxy`, `npm_config_*`, `GIT_SSH*`, `AWS_*` credential variables, `AGENTBOX_*`, `AB_*` and similar) are refused. Names that only carry a token, such as `GH_TOKEN`, are allowed. |
| `config_env` | Optional, up to 16 `NAME: value` pairs set next to the token (a URL, an org). Values up to 1024 bytes, no control characters, no name also in `env`. **Not secret**: it shows in the UI, in `agentbox-env --list` and possibly in audit rows. Never put a secret here. |
| `egress` | Up to 16 hosts the entry's boxes may reach. Each is an exact lowercase DNS name: no `*.` wildcard (a shared domain like `*.atlassian.net` would open every tenant) and no IP address. See [self-hosted services](#self-hosted-services) for `port`, `private` and `cidr`. An entry with no `egress` can only serve as a GitHub PAT owner. |
| `mcp` | Optional. A box-side MCP server that uses this token: `server` (`^[a-z0-9][a-z0-9_-]{0,62}$`, unique across entries), `command`, `args` (up to 32, none containing `${`) and `extra` (up to 4 other entries the server also reads; each must be bound to the same profile). |
| `provider`, `label`, `description` | Optional, for the UI and `--list`. `provider` is `^[a-z0-9][a-z0-9-]{0,31}$` and groups the picker. `label` is at most 32 characters, `description` at most 200. |
| `prod` | Optional. Shows a "production" chip in the picker and a `[prod]` mark in `--list`. A label only: it changes no behaviour. |

**2. Bind it to a profile.** Set `"runtime": "box"` explicitly, then tick the entries in the profile form's **Tokens** block, or write the names in `config.json`:

```json
{ "name": "claude-ops", "bin": "claude", "runtime": "box", "tokens": ["grafana-prod"] }
```

A profile binds at most 16 entries, with no duplicates, and each must be defined. A name that would set a variable the profile already sets (in `env`, `secrets` or an option's env) is refused. A bound session's box is keyed by its token entries, so changing an entry gives the profile a new box.

## How the agent uses it

```sh
agentbox-env grafana-prod -- sh -c 'curl -H "Authorization: Bearer $GRAFANA_SERVICE_ACCOUNT_TOKEN" ...'
agentbox-env NAME [NAME...] -- CMD [ARGS...]   # run CMD with those tokens in its environment only
agentbox-env --list                            # what is bound; never a value
```

`agentbox-env` asks the broker for each named token, then runs `CMD` (no shell, so a `$VAR` you type is expanded by your own shell before the token exists; wrap such a command in `sh -c '...'`) with the token and the entry's `config_env` in that one process's environment. Nothing is fetched until it runs; if any fetch fails, or two named entries would set the same variable, nothing runs. `--list` reads `/opt/agentbox/tokens.json`, which holds names, labels, hosts and `config_env` but no token.

One box can hold several environments of the same service. Give each its own entry, with distinct `mcp.server` names, and bind both:

```json
{ "name": "sentry-staging", "provider": "sentry", "value": { "type": "file", "path": "/etc/agent-box/tokens/sentry-staging" },
  "env": ["SENTRY_ACCESS_TOKEN"], "egress": [ { "host": "sentry.io" } ],
  "mcp": { "server": "sentry-staging", "command": "sentry-mcp" } },
{ "name": "sentry-prod", "provider": "sentry", "prod": true, "value": { "type": "file", "path": "/etc/agent-box/tokens/sentry-prod" },
  "env": ["SENTRY_ACCESS_TOKEN"], "egress": [ { "host": "sentry.io" } ],
  "mcp": { "server": "sentry-prod", "command": "sentry-mcp" } }
```

The two share a variable name, which is fine: the agent picks one per command (`agentbox-env sentry-prod -- ...`). Naming both in one command is refused.


**MCP servers.** An entry's `mcp` adds a server to a per-session MCP config. The server is started through `agentbox-env`, so it gets its token only for its own process. The merge order is image servers, then entry servers, then your host servers: the first one with a given name wins and the others are listed as dropped. Your host MCP config is not modified, and host servers that need tokens are still left out of boxes. A session's config that carries your personal servers is mounted read-only at `/opt/agent-session-mcp` and only into boxes of an account that inherits your personal config. Accounts that do not inherit it get the entry and image servers only. An entry is also left out, with a reason, when the box image lacks `agentbox-env` or the server's command.

<a id="self-hosted-services"></a>**Self-hosted services.** Boxes reach other networks only through the egress proxy, on port 443 and public addresses. Two optional fields on an `egress` item relax that for one exact host:

| Field | Meaning |
|---|---|
| `port` | A TLS port other than 443 (1 to 65535; unset or 0 means 443). The proxy only tunnels with `CONNECT`, so this is for TLS services. There is no plain HTTP. |
| `private` and `cidr` | Allow this host to resolve to a private address. They go together, and `cidr` must lie inside `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` or `fc00::/7` (and not wider than that range; for IPv6 at least `/32`). `100.64.0.0/10` is refused because it holds Tailscale. The box's own subnet, the host's addresses and loopback are never reachable, whatever `cidr` says. Each tunnel opened through an exception is audited as `allowed_private`. |

```json
"egress": [ { "host": "grafana.corp.example", "port": 8443, "private": true, "cidr": "10.20.0.0/24" } ]
```

**Residual:** an exception trusts DNS for that one name. Whoever controls it can point it at any address inside `cidr`, so make `cidr` as narrow as the service. `culibox doctor` warns on a range wider than `/16` (IPv4) or `/48` (IPv6). The proxy dials from the host, so a VPN route on the host applies to these hosts.

**GitHub personal access tokens.** A `github` owner can name a token entry instead of an App: set `"token": "<entry>"` and leave `app` out (exactly one of the two). The entry's `value` is the PAT. It works for `git` and `gh` like an App, with these differences:

- A PAT is not narrowed by CuliBox. The binding's `permissions` do not apply, and the token may reach more than the listed repositories. `repos` is only checked by the broker: a request has to name one of them, and one that names no repository is refused with `name the repository: a personal access token is not scoped by CuliBox`. The `gh` wrapper fails closed when it cannot read the repository from `-R`.
- Nothing is revoked when the session ends: CuliBox cannot revoke a PAT.
- Each fetch counts against the same 60-per-minute limit as `agentbox-env` (App tokens keep their own 10 per minute) and writes an audit row.
- The profile form's owner picker offers PAT entries in a "Personal access tokens" group and shows the same caveat.

When you create the token on GitHub, make it a fine-grained one: only the repos you need, Contents read and write, Pull requests read and write, Metadata read; avoid Administration and "All repositories". CuliBox cannot read a fine-grained token's permissions or repository list; scope it on GitHub.

**Audit.** Every fetch writes a row to `audit.db` (session, profile, entry name, outcome) before the value leaves, never the value; if the row cannot be written, no token is returned. That is one row per `agentbox-env` call and per `git` or `gh` call with a PAT owner. There is no pruning yet, so the table grows with use; plan for it.

## Limits and operations

- A running session keeps the entry it was spawned with, even across a daemon restart. Deleting an entry stops its fetches but not its egress; kill the session to cut an agent off.
- `static_tokens` is read at daemon start only: adding, removing or editing an entry needs a restart. The token itself is read at each fetch, so rotating a token file takes effect with no restart. An `env` reference is the daemon's environment, so changing it needs a restart too.
- A session may fetch at most 60 tokens a minute; more are refused with a rate-limit error.
- `culibox doctor` prints a line per entry: whether its reference resolves (a file problem fails; an `env` variable unset in doctor's own shell is a warning, because the daemon reads its own environment) and a warning for a wide private `cidr`.
- The box image must ship `agentbox-env` and the MCP server binaries your entries use (Grafana, Sentry, Atlassian): use an image built from this release and set `box_image` to it.
