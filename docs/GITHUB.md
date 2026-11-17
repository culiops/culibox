# GitHub access for agents

A boxed profile can carry a `github` binding. The credential broker then mints short-lived GitHub App installation tokens for the repositories you list, read-only unless you allow write on contents, pull requests or issues, so the agent can `git clone`, `git fetch`, `git push` (when write is allowed) and use `gh` without a personal access token in the box. A bound profile is refused on the host tier. On macOS its box goes through the same egress allowlist as on Linux (the allowlist is verified on a Mac; minting GitHub tokens there is not yet), and an existing VM needs `culibox box setup -recreate` once (see [docs/MACOS.md](MACOS.md#every-box)).

**1. Create the GitHub App.** The in-app guide (**How to create the App**, under Settings › Credentials › Add credential › Add GitHub › GitHub App) lists the permissions to tick. Give it read access, and add write on Contents, Pull requests and Issues only if agents should push and open PRs. Make it public ("Any account") if you will install it on more than its owner account; otherwise create it under the org that owns the repos. Install it on each owner you want to reach and select the repositories. Generate a private key and note the App's client id.

**2. Add the App to CuliBox.** On the same screen, give the App a name, enter its client id and paste the private key (the whole `.pem`, BEGIN and END lines included). The key goes into the [encrypted store](STORE.md) as `gh-<name>-key` and is never shown again; if there is no store yet the screen offers to create it. The App takes effect at once: no restart is needed. (Choosing **Personal access token** on the same screen is the simpler, less safe route; see the GitHub personal access tokens paragraph in [Static tokens for agents](STATIC-TOKENS.md).)

An App can also be declared in `config.json` (see the `github_apps` row in [Configuration](CONFIG.md)), which is read at daemon start, so adding or editing one there needs a restart. The Credentials screen lists it as defined in `config.json` and does not edit or delete it. To keep the key in a file instead of the store, choose **Advanced: use a file instead** on the screen (or use `config.json`): a regular file (not a symlink) owned by the daemon user, with mode `0600` or tighter.

```json
"github_apps": [
  { "name": "my-org", "client_id": "Iv23liXXXXXXXXXXXXXX",
    "private_key": { "type": "file", "path": "/home/me/.config/culibox/my-org.pem" } }
]
```

`private_key` is the same reference type as a static token's `value`, so `{"type": "env", "var": "<NAME>"}` and `{"type": "store", "id": "<id>"}` work too.

The key is never copied into a box. For a key file, CuliBox reads it once per daemon run and caches it after the first load, so after you rotate or change the key file, restart CuliBox. When a bound session spawns, CuliBox checks that the App is configured and its key loads (no network call), and refuses the spawn if not.

**3. Add the binding to a profile.** Set the profile's runtime to the box, then fill in the GitHub section of the profile form. In `config.json` or the profile's JSON, set `"runtime": "box"` explicitly and write the binding:

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

The box image must include `gh`: use the image `culibox box setup` builds, or `ghcr.io/culiops/culibox` pinned by digest, and set `box_image` to it (see `box setup` in [From source (manual)](INSTALL.md#from-source-manual)).

## How the agent uses it

At start, CuliBox tells a boxed Claude session which credentials it has: the names, which are PROD and how to use each kind. It never tells it a value, an account or a role. A personal access token is described as having its own scopes, because CuliBox does not narrow it. `agentbox-env --list` shows the details:

```sh
$ agentbox-env --list
GitHub
culiops [app]
  repos: agent-box, tech-advisor
  permissions: write issues · read contents, metadata
  use: git and gh as usual
```

An owner bound with a personal access token shows `permissions: the token's own (not narrowed by CuliBox)`.

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
