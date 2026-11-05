# Configuration

Everything CuliBox writes lives in one data root: `$AGENT_BOX_HOME` (an absolute path), else `~/.local/share/agent-box`. The config is read from `<data root>/config.json`, or from `-config <path>`. A `config.json` in the current directory is deliberately never read. The data root and config file must be owned by you and not writable by others.

| Key | Default | Meaning |
|---|---|---|
| `bind_addr` | `127.0.0.1:8899` | Listen address. Required when you write a config file. |
| `password_env` | unset | Name of an env var holding the login password. Unset means a generated login token. |
| `allow_public` | `false` | Must be `true` to bind all interfaces. Read the [security model](SECURITY.md) first. |
| `allowed_hosts` | `[]` | Extra `Host` values to answer to, such as a reverse proxy's hostname. |
| `default_cwd` | your home | Default workspace. Must not be your home when anything runs boxed. |
| `default_runtime` | `box` | Tier for agents that do not set their own: `box` or `host`. With no config file at all, the built-in default is `host`. |
| `box_image` | `culiops/agent-box:base` | Image used for boxes. Set it to a dated tag to pin an instance; CuliBox never rebuilds or pulls a pinned tag. A digest or registry ref is pulled by `box setup` and never built; a box spawn never pulls. |
| `box_idle_ttl_seconds` | `900` | How long a box outlives its last session. |
| `audit_egress_retention_days` | `90` | Days of egress audit rows to keep; a daemon job prunes older ones once a day (at start, then every 24 h) while egress control is running; without it nothing is pruned and the Audit page says so. `0` keeps them forever (unset is `90`, not forever). Range 0-36500. Every other audit table is kept forever. |
| `box_pids_limit` | `512` | Max processes and threads per box. |
| `box_tmp_size` | `512m` | Size of the box's `/tmp` tmpfs; number + k/m/g. |
| `box_memory` | half of docker's memory | RAM cap per box; number + k/m/g, at least `1g`, or `max` for all of docker's memory (on macOS, the VM's). Unset is half of what `docker info` reports, but at least `4g` (or all of it, if docker has less than 4g), so a small host never gets less than the old default; it is `4g` if docker cannot be asked. `max` refuses to start a box rather than run uncapped when it cannot be read. Swap is capped to the same value, so a box over its cap is killed rather than swapping the host. |
| `box_cpus` | unset (no cap) | CPU cap per box, passed as `--cpus`: `0.01` to `1024`, at most 2 decimals. Unset lets a box use every core docker has. A value above docker's core count is refused by docker when the box starts. |
| `docker_bin` | `docker` | Docker CLI to use. |
| `bundle_source` | auto-detect | Agent config to bundle into boxes; `off` disables it. |
| `data_dir` | the data root | Where state lives. |
| `abduco_bin` | `abduco` | Path to abduco. |
| `secret_sources` | none | List of `{"type": "env"}`, `{"type": "file", "path": ...}` or `{"type": "store"}` (at most one). Tried in order, first hit wins. |
| `store.unlock_ttl` | `168h` | How long an unlocked [store](STORE.md) stays unlocked (Linux). A Go duration, 1h to 720h. |
| `max_upload_bytes` | 64 MiB | Per uploaded file. Zero or negative means the default, never unlimited. |
| `max_archive_bytes`, `max_archive_files` | 512 MiB, 20000 | Caps on folder downloads. |
| `analytics.enabled` | `true` | Token and cost analytics. |
| `box_vm_cpus`, `box_vm_memory_gib` | all cores, half the RAM | macOS box VM size. Unset is every core of the Mac and half its RAM in whole GiB, at least 4, chosen when the VM is first set up; an existing VM keeps its size until `box setup -recreate` (see [macOS](MACOS.md)). |
| `broker_port` | `9071` | macOS only: credential broker port on `127.0.0.1`. |
| `extra_ca_certs` | none | Absolute paths to CA certificate PEM files that boxes should trust, for a TLS-inspecting proxy. See [Behind a TLS-inspecting proxy](MACOS.md#behind-a-tls-inspecting-proxy). Read at daemon start only: a change needs a restart. |
| `github_apps` | none | GitHub App connections the credential broker mints installation tokens with. Read at daemon start only: a change needs a restart. |
| `static_tokens` | none | Long-lived token entries (a Grafana, Sentry or Atlassian token, a GitHub personal access token) that a boxed profile can pick by name. See [Static tokens for agents](STATIC-TOKENS.md). Read at daemon start only: a change needs a restart. |
| `aws_connections` | none | Named AWS connections (source profile, role, region, optional write role) that a boxed profile can pick by name, so one session reaches several accounts. See [Several AWS accounts in one session](AWS.md#several-aws-accounts-in-one-session). Read at daemon start only: a change needs a restart. |
| `replies_enabled` | `false` | Lets the needs-you card answer permission prompts and send a one-line reply. Claude Code sessions only. Restart to apply. |
| `env_label`, `env_color` | unset | Badge shown in the UI. |

`box_pids_limit`, `box_tmp_size`, `box_memory` and `box_cpus` apply to boxes created after a restart; a running box keeps its limits until it is recreated.

Profiles made or edited in the UI are saved to `profiles.json` in the data root (or at the path set by `profiles_path`) and override a profile of the same name from `agents`. CuliBox reads that file when it starts. You can edit it by hand while CuliBox runs, but the next save or delete of a profile from the UI is then refused until you restart CuliBox, which loads your edit. A save from an editor that was opened before another change to the same profile is refused too: reload the profile and save again.

Each entry in `agents` takes `name` and `bin` (required), plus `args`, `base`, `env`, `secrets`, `options`, `runtime`, `default_cwd`, `account`, `aws`, `aws_connections` (see [Several AWS accounts in one session](AWS.md#several-aws-accounts-in-one-session)), `github` (see [GitHub access for agents](GITHUB.md)), `tokens` (see [Static tokens for agents](STATIC-TOKENS.md)), `clone_of` and `clone_share` (see [Clone mode](CLONE-MODE.md)), `context_paths` (see [Reading other repos](CONTEXT-PATHS.md)), `imports_off` (see [CLAUDE.md imports](CLAUDE-MD-IMPORTS.md)), `egress_allow` and `egress_open` (below). `base` is the environment policy: `inherit` (the default; the daemon's environment minus Claude session markers), `clean` (a minimal environment with only what you grant by name) or `login` (run through `$SHELL -l`). `egress_allow` is a list of hosts (exact, or `*.suffix`; at most 32) added to the egress allowlist of a box that has a credential binding; `egress_open: true` lets such a box reach any public host on port 443 (private, tailnet and host addresses are still refused, and every CONNECT is still audited). Both need `runtime: box`, are part of the box key (profiles with different policies never share a box), and have no effect without a binding, since an unbound box already reaches any public host; saving a profile that sets either without a binding returns a warning. Go in a bound box: `proxy.golang.org` redirects larger module zips to `storage.googleapis.com`, which is not in the default allowlist because it serves every Google Cloud Storage bucket (see [SECURITY.md](SECURITY.md)); add `storage.googleapis.com` to `egress_allow` if `go mod download` fails on such a module. Top-level `options` define the launch toggles. A profile only honours per-spawn overrides for options it lists, with one exception: an enum option with `offer_unset` is pickable on every profile whose `bin` it applies to, starting blank (the agent's own default). `offer_unset` defaults to true for `model`, `effort` and `effortLevel` and false for every other key; set it to false to turn that off, and never set it on an enum whose choices grant a capability (a permission mode). Bool options are never offered unset; `POST /api/sessions` reports any it ignored in an `options_dropped` list of `{key, reason}` in the 201 body. Images are instance-wide: set `box_image`. A profile carrying `box.image` is refused on save and ignored, with a log line, when loaded from an older store or config.
