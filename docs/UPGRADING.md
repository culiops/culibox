# Upgrading CuliBox

What an upgrade keeps, what it may reset, the backup `culibox serve` takes for you, and how to go back. `<R>` is the data root: `$CULIBOX_HOME`, else `~/.local/share/culibox` (see [Configuration](CONFIG.md)). `<B>` is one backup folder, `<R>/backups/<name>`.

The rule: **rolling back means restoring a snapshot.** Restoring returns the data root to how it was just before the upgrade, and whatever was written after it (new sessions, audit rows, profile edits) is lost. CuliBox does not promise that an older build runs on data a newer build wrote.

## How to upgrade

1. **Finish work you cannot lose.** Sessions normally survive a restart, but surviving an upgrade is not guaranteed. On macOS, read the upgrade notes in [macOS](MACOS.md) first: recreating the box VM ends boxed sessions.
2. **Install the new build, keeping the old binary.**
   - With `setup.sh` (recommended): run it again, the same way you installed (`sh setup.sh`, or `sh setup.sh --version vX.Y.Z`). It keeps the binary it replaces as `culibox.prev` next to `culibox` and prints the line that puts it back.
   - By hand (a downloaded release or your own build): copy the current binary to `culibox.prev` next to it (`cp -p culibox culibox.prev`), then put the new binary in place.
3. **Restart CuliBox** (stop the old `culibox serve`, then start the new one, or restart its service). On its first start, the new version backs up the data root before `serve` changes anything else in it (see [The automatic backup](#the-automatic-backup)) and logs the line that restores it. If the backup cannot be made, it refuses to start and says why.
4. **Run `culibox doctor`.** On macOS it says when the box VM must be recreated (`culibox box setup -recreate`).

## What an upgrade keeps

Every row is a file or folder CuliBox writes. "Backup" says whether the automatic backup copies it; "Deleted" says whether an upgrade may delete it. Paths are relative to `<R>`.

| Path | Holds | Format | An older or newer build reading it | Backup | Deleted |
|---|---|---|---|---|---|
| `config.json` | Your config (`culibox init` writes it) | JSON, no version | Unknown keys are ignored | Yes, when it is in `<R>` | No |
| `profiles.json` | Profiles made in the UI | JSON list (format version 1, no version key); a later format is `{"version": N, "profiles": [...]}` | Unknown fields are ignored (and lost on the next save) at the version this build knows. A file that does not parse is left in place: `serve` starts with the profiles from `config.json` only and refuses profile edits until the file is fixed and CuliBox is restarted. A higher version is left in place too, and `serve` loads no stored profile, refuses profile edits and refuses to start any session, config profiles included, because a stored profile may override a config one; `culibox doctor` shows a FAIL row. The message names the file, the version and this build's version, and points here | Yes | No |
| `credentials.json` | AWS connections, GitHub Apps, static tokens | JSON, format version 1 (no `version` key) | A higher `"version"` makes this build load no entry at all, so nothing reaches the broker, and the Credentials screen goes read-only; the message names the file and the version and points here. At the version this build knows, entries it fully understands are used. An unknown top-level key, a list of the wrong type, or an entry with an unknown field makes the Credentials screen read-only until the file is fixed and CuliBox is restarted. Such an entry is never used, and the file is never moved aside or saved over | Yes | No |
| `accounts.json`, `accounts/<name>/claude/`, `bin/` | Agent accounts; the files CuliBox writes in each account's Claude folder; the statusline script | JSON list, no version | A file that does not parse stops `serve` | Yes, only `settings.json`, `.agentbox-manifest.json`, `agentbox-mcp.json`, `.agentbox-statusline-delegate` and `bin/`. Not the transcripts, not the Claude login | The scripts and manifest files are rewritten at start; nothing is deleted |
| `tokens.json` | Named access tokens, as hashes | JSON list, no version | A file that does not parse, or a record with a field this build does not know, stops `serve` | Yes | No |
| `sessions.json` | What a restart needs to recover a session | JSON, no version | A file that does not parse is logged and `serve` starts without it | Yes | Entries go when a session is removed; the file stays |
| `session-ids.json` | Highest session number issued per agent | JSON, no version | Unreadable is logged and ignored; ids are kept unique from the audit log | Yes | No |
| `boxed-workspaces.json` | Folders that must run boxed | JSON, `version` 1 | An unknown version or a damaged file stops `serve` (it fails closed). For a newer version the message says it was written by a newer CuliBox | Yes | No |
| `clone-dirs`, `clone-bases/`, `clone-imports/`, `clone-return/` | Clone mode's records of which folders are clones and what was put in them | Text and small files named by a hash of the clone folder, no version | A record that does not parse is an error for that clone's operation, never trusted | Yes | No |
| `store/` | The encrypted value store: `recipient`, `identity.age`, `values/*.age` | age files | Not versioned | Yes, as bytes. CuliBox never opens it to back it up | No |
| `login.token`, `cookie.secret`, `broker.secret`, `egress.secret`, `relay2.secret` | Login token and signing keys | One 32-byte or 43-character value, mode 0600 | An empty file is replaced by a new one. A value in a format this build does not know is refused and never replaced. `serve` stops for `cookie.secret`, for `broker.secret` (when boxes are available) and for `login.token` (without `password_env`). Boxed sessions are refused for `egress.secret` and for `relay2.secret` (read only on macOS). A file that cannot be read stops `serve`, except `egress.secret` and `relay2.secret`, whose read failures only refuse boxed sessions | Yes | `relay.secret` (an old macOS key) is deleted at start |
| `audit.db` | Audit log (credentials, approvals, store, egress) | SQLite, WAL, no version number | Tables and columns are added when missing. Reading a newer file with an older build has not been tested | Yes, as a consistent snapshot (see below) | No |
| `analytics.db` | Usage numbers rebuilt from transcripts | SQLite, `user_version` 2 | An older file is rebuilt from the transcripts, after a `.pre-dedupe.bak` copy | No: derived data | The rollups may be dropped and rebuilt |
| `vm-shares.json` (macOS) | Folders the box VM shares | JSON, `version` 1 | A newer version is left alone and the message says it was written by a newer CuliBox. Any other unknown version counts as damaged. In both cases the VM starts with no folders and changes are refused | Yes | No |
| `inplace-locks.json` (macOS) | Records of files locked against a box | JSON, `version` 2 (version 1 still loads) | Records from a newer version are left in place, and in-place boxes that need a lock are refused. Other unreadable records are kept as `inplace-locks.json.unreadable` | Yes | No |
| `last-version`, `backups/` | The version that last ran, and the backups below | One line of text; directories | A build without this feature ignores both | `last-version` yes, `backups/` no | `backups/` is pruned (see Retention) |
| `boxstate/` | One home folder per box: transcripts, resume data, the box's files | Whatever the tools in the box write | Not read by CuliBox | No: it is large (gigabytes) and no upgrade changes it | See "never touches" below |
| `bundle/`, `broker/`, `ca/`, `clone-fetch/` | Caches and per-session files | Rebuilt as needed | Not read across versions | No: they are recreated | Yes (see below) |
| `vm/`, `docker/` (macOS) | The box VM's disk and the Docker client config for it | colima | Reset by `culibox box setup -recreate` | No | Only by `-recreate` |
| `serve.lock`, `sessions/pids/`, `abduco/`, `.culibox-move` | Runtime state, and the journal of a data root move | Not data | None | No | Yes |

Outside `<R>`:

- **Workspaces and clones.** The folders at `default_cwd` and every clone folder. An upgrade does not touch them, and the backup does not copy them: it holds only the records in `<R>` about clones.
- **The store key.** Linux: the unlocked key sits in the kernel keyring and is gone after a reboot; the passphrase unlocks `identity.age`. macOS: the master key is in the login Keychain (service `agent-box-store`, named by a hash of the data root path). It is not part of the backup.
- **The XDG config.** `~/.config/culibox/config.json`, when you keep your config there. No upgrade rewrites it, and the backup does not copy it.

Tested: on a data root filled through the previous release (host tier, no box VM), an upgrade changes no file outside a short allowlist (runtime state, the backup, the version stamp, and rows compared by content), and the automatic backup holds the pre-upgrade bytes. A binary-only rollback reads the data back, also after a column is added to every audit table. `scripts/upgrade-test.sh` runs in `scripts/test.sh` and in CI on Linux for every push to main and every release. It has not been run on macOS yet.

## What an upgrade may reset

- **The box VM and its images** (macOS), when a release changes how the VM is provisioned: `culibox doctor` says so, and `culibox box setup -recreate` deletes and rebuilds the VM. See [macOS](MACOS.md).
- **The box image.** After you upgrade a release binary, the next `culibox box setup` pulls the new published image when the Dockerfile changed (with no `box_image` set), and builds it locally if the pull fails. See [INSTALL](INSTALL.md#from-source-manual).
- **Old box images.** After it has pulled or built the image you configured, `culibox box setup` removes the old box images a registry can supply again and the one it replaced (the old `:base`, with the dated tag it carried: an instance pinned to that tag from a config `box setup` cannot see needs `box setup -rebuild` and its `box_image` updated to the new dated tag), and lists (without removing) the other unused ones built here, which cannot be pulled again: `culibox box setup -prune-local` removes all of those, `-no-prune` keeps everything. It always keeps an image a saved backup's config pins as a dated tag or `:base`, which is what the restore in [Rolling back](#rolling-back) needs. Details and the full list of what is kept: [INSTALL](INSTALL.md#from-source-manual).
- **Idle boxes.** Boxes with no live session are stopped when `serve` starts. A box's home in `boxstate/` stays.
- **The bundle cache** (`bundle/`): the filtered copy of your Claude config that boxes see. It is rebuilt at the next spawn, and copies unused for 14 days are removed.
- **Usage numbers** in `analytics.db`, which are rebuilt from transcripts.

**What an upgrade never touches:** `boxstate/`, your workspaces and clones, the store, and the agent accounts' transcripts and Claude login. One caution about `boxstate/`: that is not the upgrade but routine housekeeping. While `serve` runs, it removes the home of a box that no longer exists after 30 days unused, and prunes the regenerable caches in it (Go build cache, npm cache) after an hour. Back up a box home yourself if you need it longer.

## The automatic backup

`culibox serve` takes a backup when the version it runs differs from the version recorded in `<R>/last-version` (one line, `<version> <commit>`).

- **When.** Right after it takes the instance lock and before `serve` changes anything else in `<R>`. The one exception is the data root move from the old agent-box name, which runs first, so the backup holds the moved root.
- **If you install with `scripts/setup.sh`,** it runs `culibox init` after the install, and that runs before the new `serve` first starts. `init` opens the agent accounts and refreshes the files CuliBox writes in them: the statusline script in `bin/`, the `statusLine` entry in each account's `settings.json` (only when none is set or it is already CuliBox's), `.agentbox-manifest.json`, and, for an account that inherits your personal login, the links, `agentbox-mcp.json`, `.agentbox-statusline-delegate` and the settings keys copied from your default account. So the backup holds those files as `init` wrote them, not as the old build left them; the old build rewrites them at its next start. Anything else `init` changes in `<R>` (`config.json`, after it shows you the change and asks; the store and its token; `accounts.json`, when it creates the `claude-box` account; audit rows for store changes; the box setup) belongs to the steps it prints and asks about, and does not run on its own.
- **No backup:** a build without a version (a development build), and a fresh install (none of the backed-up files exist yet). A development build writes nothing. A fresh install writes only `last-version`. A missing `last-version` counts as an upgrade from `unknown`. A downgrade is backed up like any other change.
- **Where.** `<R>/backups/<from>-to-<to>-<UTC time>/`, mode 0700. It mirrors `<R>`, with file modes kept, so restoring is a plain copy. It is built under a `.tmp-` name and renamed when complete. A start logs the path and the restore command.
- **What it holds.** The files marked "Yes" above. `audit.db` is copied with SQLite's `VACUUM INTO`, which includes rows still in the WAL file. Only regular files are copied: a symlink or any other type at one of these paths is skipped and logged, never followed.
- **What it leaves out.** `boxstate/`, `vm/`, `docker/`, `bundle/`, `broker/`, `ca/`, `clone-fetch/`, `analytics.db`, `.culibox-move`, runtime state, the transcripts and the Claude login inside accounts, symlinks inside an account's Claude folder (the account setup at start makes them again), and anything outside `<R>`.
- **Size bound.** An `audit.db` over 512 MB is not snapshotted: the log names its size and skips it. Restoring then keeps your current `audit.db`.
- **Retention.** After a backup completes, every backup from the last 30 days is kept, the 3 newest are always kept, and at most 20 are kept (the oldest go first). Retention looks only at folders in `backups/` whose name ends in `-<UTC time>` (for example `-20261113T103000Z`), whoever made them, and may remove them. A folder or file with any other name, and any name starting with a dot, is never touched.
- **Secrets are in the backups.** A backup holds old copies of the login token, the `*.secret` signing keys, the hashes in `tokens.json`, `credentials.json` and the store's encrypted files, for 30 days, or longer for the 3 newest backups, which are kept however old, and a restore brings them back. After you rotate a secret or token because it leaked, delete the backups taken before the rotation from `<R>/backups/`.
- **Fail closed.** If the backup cannot be made (disk full, a listed file that exists but cannot be read, `audit.db` that SQLite cannot read), `serve` refuses to start, before it changes anything else in `<R>`. The message names the cause and the file, points at `<binary>.prev` and at this page. `last-version` is not written, so the next start tries again. Fix the cause and start again, or put the previous binary back.

## Rolling back

0. **macOS: end the in-place boxed sessions first,** while the newer CuliBox still runs. A box started in place keeps the folder's guarded files locked on the Mac (`chflags uchg`, on a case-insensitive volume, the Mac default) until the box is gone, and the box goes when its last session ends. Then run `culibox doctor`: it should show no `in-place locks` row. If a row says a file is "still locked, but no box holding the lock runs", restart the newer CuliBox: at boot it lifts those locks, whether the box VM is stopped (colima confirms it) or running (the boot box reconcile lifts the locks of boxes that are gone). It waits for the next box reconcile or sweep only when it cannot read the VM status, or when docker cannot list the boxes. If the row stays, run the `chflags nouchg` line it prints, but only when no boxed session runs in that folder in any CuliBox on this Mac: doctor checks only this CuliBox's boxes. A row that says whether the box holding the lock "still runs cannot be checked" appears when the VM status cannot be read, or when the VM is up but docker cannot list its boxes (a VM that is confirmed stopped gives the "still locked" row instead). Do not unlock anything until you know no boxed session runs in that folder. This step is a precaution. In the one rollback that was measured (step 5), `CLAUDE.md` stayed locked under the older build and was unlocked only after the newer CuliBox ran again. Whether the older build lifts the lock by itself after its box ends was not measured. Skipping this step left nothing locked in that run after the newer CuliBox ran again, so check step 5.
1. **Stop CuliBox** (Ctrl-C, or stop its service).
2. **Put the previous binary back.** `scripts/setup.sh` keeps it as `culibox.prev` next to `culibox` and prints the line that restores it. `scripts/deploy-prod.sh` does the same for a deployed binary. Either script prints a `data:` line pointing here. Binary only: this does not undo data changes.
3. **Restore the snapshot,** to also undo what the newer build did to your data. Run the exact line `serve` logged when it took the backup (`upgrade backup: ... then run: ...`). It already matches what that backup holds, with the real folders filled in. It has one of two shapes. Here `R` stands for `<R>` and `B` for `<B>`, both written out in full in the logged line:

```sh
# the backup holds audit.db:
rm -f "$R"/last-version "$R"/audit.db-wal "$R"/audit.db-shm && cp -a "$B"/. "$R"/
# the backup has no audit.db (it was over 512 MB): audit.db is left alone
rm -f "$R"/last-version && cp -a "$B"/. "$R"/
```

   The first shape removes the `audit.db` WAL files before it copies, so that a copy that stops part-way never leaves an older `audit.db` next to a newer WAL file. If the line stops with an error, fix the cause (for example free disk space) and run it again: it is safe to run again.

4. **Start the old binary.**
5. **macOS: check for files left locked,** when you skipped step 0 or are not sure. In each folder where you ran a box in place, list the guarded files with their flags:

```sh
ls -lOd CLAUDE.md CLAUDE.local.md AGENTS.md .mcp.json .git
```

   `ls` reports the names the folder does not have; ignore those. Only a `.git` file (a worktree checkout) is ever locked, never a `.git` folder. Unlock a file that shows `uchg` with `chflags nouchg <file>` only when no boxed session runs in that folder (in any CuliBox on this Mac) and you did not lock it yourself. This is a check, not an expected result. In the rollback that was measured (a live in-place session, then the older build started with only the binary put back, and again after the restore line), `CLAUDE.md` stayed locked under the older build in both runs, and its `culibox doctor` still listed the lock. In the binary-only run, the older build also did not refuse an in-place spawn and joined the session. After the newer CuliBox started again and the boxes ended, nothing was left locked. Whether the older build would lift the lock by itself once its box ended was not measured. If you put back only the binary (no step 3), the newer lock records stay: a CuliBox that reads them lifts those locks itself when it starts, and one that cannot is in [Running an older CuliBox on newer data](#running-an-older-culibox-on-newer-data).

What to know before you run it:

- **Restore does not delete.** It copies the snapshot over `<R>` and leaves everything newer in place: accounts, store values, clone records and box homes made since the upgrade. `<R>` becomes the snapshot plus whatever is newer. Older builds ignore files they do not know.
- **It also restores `last-version`,** so the next upgrade takes a fresh backup.
- **Restoring `audit.db` drops the audit rows written since the upgrade.** The first shape also removes `audit.db-wal` and `audit.db-shm` (first, before the copy), because a newer WAL file must never be applied to the older database. Without `audit.db` in the backup, the second shape leaves the current `audit.db` and those two files untouched. To keep the current log even though the backup holds one, skip it and leave its `-wal` and `-shm` files alone:

```sh
rm -f "$R"/last-version && find "$B" -mindepth 1 -maxdepth 1 ! -name audit.db -exec cp -a {} "$R"/ \;
```

  Whether an older build reads a newer `audit.db` is not tested yet.
- **Store changes revert too.** `store/` goes back to the snapshot, so a value you set, or a passphrase you changed (`culibox store passwd`), after the upgrade is gone. The old passphrase unlocks it again.
- **Restore into the same path.** On macOS the Keychain key is named by a hash of the data root path. Restoring into a different path leaves the key behind (the same rule as a data root move), and the store cannot be unlocked there. On Linux you unlock again with the passphrase.

## Running an older CuliBox on newer data

Swapping only the binary back (the `.prev` rollback line) runs the older CuliBox on data the newer one may have changed. The supported rollback is to also restore the backup (see "Rolling back" above). If you skip that, then for the files below CuliBox v0.35.0 and later does not delete, regenerate or move aside content a newer CuliBox wrote. It says which file, and then it refuses to start, refuses the part that needs the file, or keeps running with that file read-only:

| File | What the older CuliBox does |
|---|---|
| `credentials.json` | Uses the entries it understands. The Credentials screen will not save until you fix the file and restart CuliBox, or run the newer CuliBox. |
| `profiles.json` | If it cannot read the file: starts with the profiles from `config.json` only and refuses profile edits. |
| `tokens.json` | Refuses to start if a token record has a field it does not know. |
| `cookie.secret`, `broker.secret`, `egress.secret`, `relay2.secret`, `login.token` | Refuses a secret in a format it does not know. `serve` stops for `cookie.secret`, for `broker.secret` (when boxes are available) and for `login.token` (without `password_env`). Boxed sessions are refused for `egress.secret` and for `relay2.secret` (read only on macOS). Delete the file to make a new one (for `cookie.secret` that signs every browser out). |
| `boxed-workspaces.json` | Refuses to start. |
| `vm-shares.json` (macOS) | Leaves `vm-shares.json` as it is; the folder list cannot be changed. |
| `inplace-locks.json` (macOS) | Refuses in-place boxes that need a file lock. |

## macOS

- The data root is on the Mac (`~/.local/share/culibox`), not inside the VM, so the backup and the restore work as on Linux.
- `vm-shares.json` and `inplace-locks.json` exist only on macOS. The backup includes them when present.
- Before a rollback, end the in-place boxed sessions under the newer CuliBox, and afterwards check for guarded files left locked: steps 0 and 5 of [Rolling back](#rolling-back).
- The Keychain item is not copied. A restore into the same path keeps it valid. See "Restore into the same path" above.
- Security upgrades that change the box VM: see [macOS](MACOS.md), which says when `box setup -recreate` is needed.

## From agent-box

CuliBox was called agent-box before its first public release. An install from that time keeps working with the `culibox` binary:

- **What changes.** The binary and every command are now `culibox`: replace `agent-box` in your service file or scripts. The environment variable is `CULIBOX_HOME`; `AGENT_BOX_HOME` still works and prints a notice, and if both are set to different paths, CuliBox refuses to start. New installs build the box image as `culiops/culibox:base` and pull from `ghcr.io/culiops/culibox`.
- **What moves on its own.** On the first start of `culibox serve`, the data folder `~/.local/share/agent-box` is moved to `~/.local/share/culibox`, and a link is left at the old path. Your store key, profiles, accounts and history come along. The move waits for a start when no session is running, and it does not move a folder you set with `CULIBOX_HOME`/`AGENT_BOX_HOME`. On a Mac with a box VM the folder stays where it is: Lima and colima keep absolute paths into it, so the VM cannot follow a move without `culibox box setup -recreate`. `culibox doctor` shows which folder is in use, and why (on a Mac: "kept in place: the box VM lives here; it cannot follow a move without box setup -recreate"). A `~/.config/agent-box/config.json` moves to `~/.config/culibox/` the same way.
- **Rolling back to an agent-box binary.** On a Mac, end your sessions first. The old binary names its abduco socket directory from the old folder path, so it does not see sessions that the new binary started. (On Linux the socket directory sits inside the data folder, which the link at the old path still reaches.)
- **What stays.** A `box_image` you pinned keeps working; if you never set one, your existing image is reused. Some internal names still say agent-box for every install, old or new: the Keychain and keyring entry names, the colima profile `agent-box`, container labels and browser layout settings. Nothing needs to be redone.

## For contributors: the migration rule

A migration is any code that rewrites a persisted file when a new version first reads it. The rule is **copy**: the version-change backup is the copy from before the migration, so a migration does not need its own `.bak`. A new migration must:

- run only in `serve`, after the backup step. Do not put a migration in a loader that `culibox init`, `culibox doctor`, the `store` commands or the bug report also call (for example `account.Open`, which refreshes every account's files, or the audit log opener): those run before any backup, and `init` runs right after `setup.sh` installs a new build;
- be idempotent;
- be crash-safe (write a temp file, then rename).

An older build reading what the migration wrote is **not** required to work. The exceptions are the files that a build must refuse to read when it is too old, rather than damage them (the version checks in the table above).

**Format versions: a restriction must bump the version.** `credentials.json` and `profiles.json` each carry a format version (`credfile.FormatVersion`, `store.FormatVersion`; a missing version is 1). A build that reads a higher version than it knows loads nothing from that file and refuses to save over it. For `credentials.json` no entry reaches the broker. For `profiles.json` the build also refuses to start any session, including a profile that comes from `config.json`: saving a built-in or config profile in the UI writes a same-named override into `profiles.json`, so a build that cannot read the file cannot know what that profile was tightened to. `culibox doctor` names the file, its version and this build's version; the spawn refusal and the profile save error name `profiles.json` and point here. The file is never deleted, rewritten or hidden; the way out is to run the CuliBox that wrote it again or restore the pre-upgrade backup. Together this stops a rolled-back build from running without a restriction it does not know. A `profiles.json` that merely fails to parse keeps the older behaviour (config profiles start, edits are refused). So:

- **any new key that restricts, scopes or contains something** (a revoke or deny list, containment, egress, a credential reference or scope, whether at the top level or inside an entry or profile) **must bump that file's format version in the same change**. A benign addition (a label, a display field) needs no bump;
- an explicit `"version": 1` is accepted on read (as `{"version": 1, "profiles": [...]}` in `profiles.json`) and is written back without the key, because version 1 is the file with no version key;
- the readers and writers keep the version: a rewrite writes the file's version, never a lower one. Version 1 is written without a version key (`credentials.json`) or as the plain list (`profiles.json`), so a build that knows no format versions still reads it; a higher version is written out, and those older builds then refuse it;
- the tests `TestLoadHigherVersionFailsClosed` (credfile) and `TestOpenHigherVersionFailsClosed` (store) pin the refusal, `TestNewerProfilesFileRefusesConfigSeedSpawn` (web) pins that a config profile cannot start while `profiles.json` is newer, and `TestRewriteKeepsFutureVersion` in each package pins the write rule.

Two more rules keep a binary-only rollback safe:

- do not move or rename `store/recipient`. An older `store init` that finds none creates a new identity over `identity.age`;
- put a restriction on existing `credentials.json` entries inside the entries, never in a new top-level key. A build with the strict per-entry decode (v0.35.0 and later) ignores a key it does not know and still uses the entries it understands, so the restriction would be lost. Inside an entry, the strict per-entry decode makes that build refuse the entry.

The data root move (the agent-box to culibox rename) runs before the backup by design: the backup holds the moved root, and its rewriters (the `config_dir` in `sessions.json`, the account `statusLine`, the `analytics.db` rows) have already run.

When you add a file to the data root, add it here with its writer, its format, how an older and a newer build treat it, and say whether the backup includes it. A file the backup should include goes in the list in `internal/upgradebackup`.
