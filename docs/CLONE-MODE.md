# Clone mode

Clone mode lets a boxed agent work on a private copy of one of your repos. Nothing the box writes reaches the original except as a `box/<branch>` branch that CuliBox brings home, by itself or through `culibox clone fetch`, for you to review. It works on Linux and, in the box VM, on macOS (see [On macOS](#on-macos)).

Two profile fields turn it on:

| Field | Meaning |
|---|---|
| `clone_of` | Absolute path of the host repo to copy. It must be a plain git repo with a `.git` directory (no worktree, no `commondir`, no `objects/info/alternates`), not a symlink, and not your home directory or any directory above it. |
| `clone_share` | Up to 8 paths, relative to `clone_of`, that the box can read. Example: `["CLAUDE.md", "docs/notes"]`. |

The profile's `default_cwd` is the clone's directory. The two must not overlap: neither may be inside the other. The profile must be boxed: `runtime: "host"` is refused, and so is a profile that sets no runtime while the instance's `default_runtime` is `host`, both when you save it and when you spawn it. The one exception is a spawn that sends `clone: false`, which runs the profile without its clone: in a box in place, or on the host only when it also sends `runtime: "host"` and nothing closes the host to the profile (bindings, context paths or `box_only`). Omitting the runtime never reaches the host. A restart keeps the choice. The first spawn creates the clone with host `git clone`. Spawn the session in the clone's root, not a subdirectory.

In the web UI, pick **Work on a private copy** in Settings › Profiles: the folder field is `clone_of`, **Copy lives in** is `default_cwd`, and **Read-only paths** is `clone_share`.

## What the box gets

| In the box | Mode | What it is |
|---|---|---|
| the clone directory | read-write | The box's own copy of the repo, and the workspace. |
| the clone's `.git` | read-write | Part of that copy. The box commits here. |
| `<clone_of>/.git` | read-only | The original's history, so `git fetch origin` inside the box works. The box cannot change it. |
| each `clone_share` path | read-only | The original's file, shown at the same path inside the clone. |
| the host's Claude memory for `clone_of`, if any | read-only | So the boxed agent starts with what you already told Claude about this repo. |

Claude's memory notes for `clone_of` are readable by the box, which has internet egress. Keep secrets out of them, or set `bundle_source: "off"`, which also turns the memory mount off.

Everything else is as in any boxed session: no host home, no Docker socket, egress only through the proxy (except [services listening on all addresses on this host](SECURITY.md#host-services)).

**Shares are read-only, and readable.** There is no writable share: a `:rw` suffix is refused by name, and so is any other `:`. A share path must be relative and clean, cannot be `.git`, cannot nest inside another share, and no part of its path may be a symlink. The box can read everything you share and has internet egress, so never share secrets. CuliBox warns for names like `.env*`, `*.pem`, `*.key` and `config*.json`, but it does not stop you. A shared `CLAUDE.md` is readable in the box but is **not** loaded as project instructions: boxed Claude runs with `--setting-sources user`. Tell the agent to read it first, or put box guidance in your user-level `CLAUDE.md`. Files the box writes for you (plans, notes) go in a directory inside the clone, such as `docs/handover/box/`, that your `.gitignore` excludes; they never travel in a fetch.

## Bringing code home

Work comes home by itself as `box/<branch>` in `clone_of`, the base branch included, when the session's agent exits and at each spawn of the clone. It moves a ref only as a fast-forward of your `box/<branch>`, and never while you have that branch checked out. When a branch cannot come home, the profile shows "N branches not home" with the reason for each. The profile field `clone_no_auto_return` turns this off (the checkbox is "Bring work home automatically", on by default); the profile then only reports what is not home. `culibox clone fetch` stays the manual override: it forces the update. "Bring work home now" in the session forces it too, but still never moves a `box/<branch>` you have checked out.

```sh
culibox clone fetch [-config <path>] [-base <branch>] [-allow-old-history] <profile> <branch>
```

**Pushing to GitHub.** When the profile's GitHub binding can push to the clone's `origin` repository (it has write on `contents`, or it uses a personal access token), CuliBox adds a push-only URL rewrite to the session's git configuration. It is set through the session's environment; nothing is written into the clone. Inside the box, `git push -u origin <branch>` then goes to GitHub, while `git fetch origin` still reads the host repo, and `gh pr create` works. The `box/*` refs come home as usual (unless auto-return is off), whether or not you push. The boxed claude is told this in one appended system-prompt line, and the session shows "work comes home as a PR". Pushes that touch `.github/workflows/` are refused by GitHub for App tokens and for PATs without the Workflows permission (see [GitHub access for agents](GITHUB.md)).

The flags come before the two names. `-base` defaults to `main`. CuliBox makes a git bundle of `<branch>` from the clone inside a throwaway container with no network and the clone mounted read-only, then verifies it and fetches it into `clone_of` as `box/<branch>`, with git's object checks on. The host never runs git against the clone's own `.git`, and never merges: the branch just appears. The command prints the commits that came, a warning for any commit message that carries an AI-attribution trailer, and the review line:

```text
fetched box/feat-x (2 commit(s) not on main):
  abc1234 add the thing
  def5678 fix the thing
review: git -C /home/me/repo log -p main..box/feat-x; merge per your review policy
```

Commit subjects are printed with control characters replaced, because a box wrote them. Bundles over 512 MiB are refused. The container runs the global `box_image`.

**Keeping the clone current.** At each spawn of an existing clone, CuliBox fast-forwards the clone's local `main` (else `master`) from the host, in a throwaway container with no network, and shows the result in the spawn toast and the steer rail. It is best effort and never fails a spawn. It skips a clone that a live session or another instance's box is using, and never touches a checked-out feature branch's work tree. `clone fetch` refuses a branch that carries history host `main` no longer has (for example after a rebase or force-push), or whose clone history CuliBox cannot verify, such as a clone with no recorded base. `clone fetch`, and auto-return, also refuse a branch that carries a commit your repo still holds but no branch or tag there reaches any more. It cannot see a commit that has since been pruned (by any means) and was never a recorded base tip, unless the latest `git filter-repo` `commit-map` names it (a second filter-repo run overwrites that map), nor a commit that was already behind a `box/*` branch when the record was first seeded. `-allow-old-history` overrides these after you have reviewed the branch. A daemon restart aborts a `clone fetch` that is in flight; run it again.

## Refusals

- A host-tier session is refused anywhere inside a clone, including a subdirectory, a path that does not exist yet, or another spelling of the same directory (a symlink, or different letter case on a case-insensitive disk such as the macOS default). The box wrote those files, and host-tier tools would trust them. A host-tier session at `clone_of` or at a share source is fine.
- CuliBox records every directory it has used as a clone in `<data dir>/clone-dirs`, one path per line. A recorded directory stays box-owned even after its profile is edited or deleted. Deleting the directory does not release it. To release one, remove its line from `<data dir>/clone-dirs`. An unreadable record refuses every host-tier spawn until it is fixed.
- A spawn is refused, never partly started, if `clone_of`, a share source or a mountpoint is unsafe: a symlink on the path, a path inside the data root or a config directory, or an existing clone whose `origin` is not `clone_of`.
- A `clone_of` that is inside another profile's clone, or inside a recorded clone directory, is refused when you save the profile, when you spawn it and in `clone fetch`: the owning box could have planted git hooks there.
- Changing `clone_of`, `default_cwd` or `clone_share` gives the profile a new box rather than reusing the old one.

## On macOS

Clone mode works as on Linux, with the boxes in CuliBox's box VM (see [Running CuliBox on macOS](MACOS.md#boxed-tier-contained-sessions)). The containers that refresh a clone, list its branches and make the bundle for `clone fetch` run in that VM too, never on your own docker.

- `clone_of` must be inside a VM folder (read-only is enough) and the copy's parent directory inside a read-write one: the VM shares only the folders on its list (see [VM folders](MACOS.md#vm-folders)), and the data root cannot hold either. Any other place is refused when you save the profile and when you spawn it, before the copy is made.
- `culibox clone fetch` needs the box VM running and never starts it. With the VM stopped it refuses and tells you to run `culibox box setup` (or start any boxed session) first.
- Leftover clone containers are removed when `culibox serve` starts and the VM is running. While the VM is stopped no container runs in it.
- Files the box writes in the clone are owned by your Mac user.

## Known limits

- When branches come home, CuliBox flags executables, disguised files and other files to review, in the toast and in `culibox clone fetch`. It is a signal to review, not a malware scan; see [docs/SECURITY.md](SECURITY.md).
- Never put `clone_of` inside a boxed workspace (or any other directory a box can write): a box could plant git hooks in that repo, and the host runs git there during `clone fetch`. CuliBox refuses a `clone_of` that is inside a clone or a recorded clone directory, and runs its own git calls with hooks disabled, but it cannot see every directory a box can write. A spawn that runs a profile in a box in place (`clone: false`) at your `clone_of` makes that repo a boxed workspace: the agent in the box can write its files and its `.git`. CuliBox's own git runs there with hooks and fsmonitor off, but review that repo before you run host git in it.
- Never run a host-tier agent session in a directory above the clones (for example your home). Claude Code lazily loads nested `CLAUDE.md` and `.claude/` files from any subdirectory it reads, so files a box wrote under a clone could steer a host agent. Keep clones outside every tree where host sessions run.
- Your own shell inside a clone cannot be policed. A box can plant git hooks, config and aliases in the clone's `.git`, so never run git or other host tools there. Review through `clone_of` after a fetch, not in the clone.
- A file you copy out of a clone by hand might be a symlink the box planted, and `cp` would copy its target. Check that it is a regular file first.
- The box and the host share a disk. A box can fill it; there is no per-box quota.
- CuliBox's file editor follows links inside box-written directories. Treat a clone's contents as untrusted when you open them in the UI.
