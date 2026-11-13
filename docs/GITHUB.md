# GitHub access for agents

A boxed profile can carry a `github` binding. The credential broker then mints short-lived GitHub App installation tokens for the repositories you list, read-only unless you allow write on contents, pull requests or issues, so the agent can `git clone`, `git fetch`, `git push` (when write is allowed) and use `gh` without a personal access token in the box. A bound profile is refused on the host tier. On macOS its box goes through the same egress allowlist as on Linux (the allowlist is verified on a Mac; minting GitHub tokens there is not yet), and an existing VM needs `culibox box setup -recreate` once (see [docs/MACOS.md](MACOS.md#every-box)).

**1. Create the GitHub App.** The in-app guide (the **How to create the App** guide in Settings › Credentials › Add GitHub › GitHub App) lists the permissions to tick. Give it read access, and add write on Contents, Pull requests and Issues only if agents should push and open PRs. Make it public ("Any account") if you will install it on more than its owner account; otherwise create it under the org that owns the repos. Install it on each owner you want to reach and select the repositories. Generate a private key and note the App's client id.

**2. Store the private key.** Put the PEM file where the daemon user owns it, as a regular file (not a symlink) with mode `0600` or tighter. Then declare the App in `config.json` and restart CuliBox (see the `github_apps` row in [Configuration](CONFIG.md)):

```json
"github_apps": [
  { "name": "my-org", "client_id": "Iv23liXXXXXXXXXXXXXX",
    "private_key": { "type": "file", "path": "/home/me/.config/culibox/my-org.pem" } }
]
```

The key is never copied into a box. CuliBox reads the key file once per daemon run and caches it after the first load, so after you rotate or change the key file, restart CuliBox. When a bound session spawns, CuliBox checks that the App is configured and its key loads (no network call), and refuses the spawn if not.

**3. Add the binding to a profile.** Set `"runtime": "box"` explicitly, then fill in the GitHub section of the profile form, or write it in `config.json`:

```json
"github": {
  "owners": [
    { "owner": "my-org", "app": "my-org", "repos": ["api", "web"] }
  ]
}
```

| Field | Meaning |
|---|---|
| `owners[].owner` | The account or organisation that owns the repos. |
| `owners[].app` | The `name` of a `github_apps` entry that is installed on that owner. |
| `owners[].repos` | The repositories a token may reach (up to 100 per owner). |
| `permissions` | Optional. The default is `actions`, `checks`, `contents`, `pull_requests`, `issues` and `metadata`, all `"read"`; `statuses` can be added. `contents`, `pull_requests` and `issues` may be `"write"`; every other key is `"read"` only, and any other value is refused. |
| `egress_allow` | Optional extra hosts for this binding's boxes, on top of the GitHub hosts below. |

The box image must include `gh`: use an image built from this version (tag `2026-10-19.2` or later) and set `box_image` to it (see `box setup` in the [quick start](../README.md#quick-start)).

## How the agent uses it

- `git` over HTTPS to `github.com` asks the broker for a token through a credential helper. Nothing is stored and no token is in the environment.
- `gh` is a thin wrapper that fetches a token for the repo's owner and sets `GH_TOKEN` only on the real `gh` process it starts. In a clone-mode box, `GH_REPO` is set from the host repo's `origin` when that repo is in the binding, because the box's own `origin` is a host path.
- Each token lasts one hour (GitHub's fixed lifetime for installation tokens), covers only the repos you listed and the permissions you chose, and is reused until 10 minutes before it expires.
- Two sessions with an identical binding share one box and one uid, so their audit rows are effectively per box. That is no scope escalation: both hold the same scope.
- Every mint is written to the audit log (`audit.db`) with the session, owner and installation, never the token. If that row cannot be written, no token is returned.
- Tokens are revoked at GitHub when the agent exits or you kill the session, and on a clean daemon shutdown. If the daemon crashes, they are left to expire, at most one hour later.

**Egress.** A GitHub-bound box may also reach `api.github.com`, `codeload.github.com`, `results-receiver.actions.githubusercontent.com` and `productionresultssa<N>.blob.core.windows.net` (the Actions log shards `productionresultssa0` to `productionresultssa199` except 22, never `*.blob.core.windows.net`), plus the binding's `egress_allow`. `github.com` is already in the default allowlist.

## What this does and does not protect against

- **The agent can read the token while it runs.** It can run `git credential fill`, read the `gh` child's environment, or call the broker socket itself. The helper design prevents accidental exposure only: the token is not inherited by every child process or MCP server, does not show up in `env` dumps and is not pasted into transcripts. It does not stop a deliberate read.
- The real controls are the one-hour lifetime, the repo and permission scope, the egress lock, the per-mint audit and revocation when the session ends.
- **Workflow files.** GitHub refuses a push that touches `.github/workflows/` unless the token has the Workflows permission. An App token cannot edit `.github/workflows/` (CuliBox does not request Workflows); a PAT can if you granted it. Either way, a pushed branch runs your existing push and pull-request workflows on the agent's code, with whatever secrets those workflows get. Keep secrets in protected environments or branches, or require approval for workflow runs.
- **Residual: exfiltration through `github.com`.** It is open to every bound box, so an agent could push data to a repository it controls, using credentials it brought itself. This is not new and not fixed here.
