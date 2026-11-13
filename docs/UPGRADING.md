# Upgrading CuliBox

What an upgrade keeps, what it may reset, the backup `culibox serve` takes for you, and how to go back. `<R>` is the data root: `$CULIBOX_HOME`, else `~/.local/share/culibox` (see [Configuration](CONFIG.md)).

The rule: **rolling back means restoring a snapshot.** Restoring returns the data root to how it was just before the upgrade, and whatever was written after it (new sessions, audit rows, profile edits) is lost. CuliBox does not promise that an older build runs on data a newer build wrote.

## What an upgrade keeps

Every row is a file or folder CuliBox writes. "Backup" says whether the automatic backup copies it; "Deleted" says whether an upgrade may delete it. Paths are relative to `<R>`.

| Path | Holds | Format | An older or newer build reading it | Backup | Deleted |
|---|---|---|---|---|---|
| `config.json` | Your config (`culibox init` writes it) | JSON, no version | Unknown keys are ignored | Yes, when it is in `<R>` | No |
| `profiles.json` | Profiles made in the UI | JSON list, no version | Unknown fields are ignored (and lost on the next save). A file that does not parse is moved to `profiles.json.corrupt` and the list starts empty | Yes | No |
| `credentials.json` | AWS connections, GitHub Apps, static tokens | JSON, no version | An unknown top-level key makes the file unreadable, and the next save moves it to `credentials.json.bad-<time>`. An entry with an unknown field is left out | Yes | No |
| `accounts.json`, `accounts/<name>/claude/`, `bin/` | Agent accounts; the files CuliBox writes in each account's Claude folder; the statusline script | JSON list, no version | A file that does not parse stops `serve` | Yes, only `settings.json`, `.agentbox-manifest.json`, `agentbox-mcp.json`, `.agentbox-statusline-delegate` and `bin/`. Not the transcripts, not the Claude login | The scripts and manifest files are rewritten at start; nothing is deleted |
| `tokens.json` | Named access tokens, as hashes | JSON list, no version | A file that does not parse stops `serve` | Yes | No |
| `sessions.json` | What a restart needs to recover a session | JSON, no version | A file that does not parse is logged and `serve` starts without it | Yes | Entries go when a session is removed; the file stays |
| `session-ids.json` | Highest session number issued per agent | JSON, no version | Unreadable is logged and ignored; ids are kept unique from the audit log | Yes | No |
| `boxed-workspaces.json` | Folders that must run boxed | JSON, `version` 1 | An unknown version or a damaged file stops `serve` (it fails closed) | Yes | No |
| `clone-dirs`, `clone-bases/`, `clone-imports/`, `clone-return/` | Clone mode's records of which folders are clones and what was put in them | Text and small files named by a hash of the clone folder, no version | A record that does not parse is an error for that clone's operation, never trusted | Yes | No |
| `store/` | The encrypted value store: `recipient`, `identity.age`, `values/*.age` | age files | Not versioned | Yes, as bytes. CuliBox never opens it to back it up | No |
| `login.token`, `cookie.secret`, `broker.secret`, `egress.secret`, `relay2.secret` | Login token and signing keys | One 32-byte or 43-character value, mode 0600 | A malformed file is replaced by a new one; one that cannot be read stops `serve` | Yes | `relay.secret` (an old macOS key) is deleted at start |
| `audit.db` | Audit log (credentials, approvals, store, egress) | SQLite, WAL, no version number | Tables and columns are added when missing. Reading a newer file with an older build has not been tested | Yes, as a consistent snapshot (see below) | No |
| `analytics.db` | Usage numbers rebuilt from transcripts | SQLite, `user_version` 2 | An older file is rebuilt from the transcripts, after a `.pre-dedupe.bak` copy | No: derived data | The rollups may be dropped and rebuilt |
| `vm-shares.json` (macOS) | Folders the box VM shares | JSON, `version` 1 | An unknown version counts as damaged: the file is left alone, the VM starts with no folders and changes are refused | Yes | No |
| `inplace-locks.json` (macOS) | Records of files locked against a box | JSON, `version` 2 (version 1 still loads) | An unknown version is an error; unreadable records are kept as `inplace-locks.json.unreadable` | Yes | No |
| `last-version`, `backups/` | The version that last ran, and the backups below | One line of text; directories | A build without this feature ignores both | `last-version` yes, `backups/` no | `backups/` is pruned (see Retention) |
| `boxstate/` | One home folder per box: transcripts, resume data, the box's files | Whatever the tools in the box write | Not read by CuliBox | No: it is large (gigabytes) and no upgrade changes it | See "never touches" below |
| `bundle/`, `broker/`, `ca/`, `clone-fetch/` | Caches and per-session files | Rebuilt as needed | Not read across versions | No: they are recreated | Yes (see below) |
| `vm/`, `docker/` (macOS) | The box VM's disk and the Docker client config for it | colima | Reset by `culibox box setup -recreate` | No | Only by `-recreate` |
| `serve.lock`, `sessions/pids/`, `abduco/`, `.culibox-move` | Runtime state, and the journal of a data root move | Not data | None | No | Yes |

Outside `<R>`:

- **Workspaces and clones.** The folders at `default_cwd` and every clone folder. An upgrade does not touch them, and the backup does not copy them: it holds only the records in `<R>` about clones.
- **The store key.** Linux: the unlocked key sits in the kernel keyring and is gone after a reboot; the passphrase unlocks `identity.age`. macOS: the master key is in the login Keychain (service `agent-box-store`, named by a hash of the data root path). It is not part of the backup.
- **The XDG config.** `~/.config/culibox/config.json`, when you keep your config there. No upgrade rewrites it, and the backup does not copy it.

## What an upgrade may reset

- **The box VM and its images** (macOS), when a release changes how the VM is provisioned: `culibox doctor` says so, and `culibox box setup -recreate` deletes and rebuilds the VM. See [macOS](MACOS.md).
- **Idle boxes.** Boxes with no live session are stopped when `serve` starts. A box's home in `boxstate/` stays.
- **The bundle cache** (`bundle/`): the filtered copy of your Claude config that boxes see. It is rebuilt at the next spawn, and copies unused for 14 days are removed.
- **Usage numbers** in `analytics.db`, which are rebuilt from transcripts.

**What an upgrade never touches:** `boxstate/`, your workspaces and clones, the store, and the agent accounts' transcripts and Claude login. One caution about `boxstate/`: that is not the upgrade but routine housekeeping. While `serve` runs, it removes the home of a box that no longer exists after 30 days unused, and prunes the regenerable caches in it (Go build cache, npm cache) after an hour. Back up a box home yourself if you need it longer.

## The automatic backup

`culibox serve` takes a backup when the version it runs differs from the version recorded in `<R>/last-version` (one line, `<version> <commit>`).

- **When.** Right after it takes the instance lock and before `serve` changes anything else in `<R>`. The one exception is the data root move from the old agent-box name, which runs first, so the backup holds the moved root.
- **No backup:** a build without a version (a development build), and a fresh install (none of the backed-up files exist yet). A development build writes nothing. A fresh install writes only `last-version`. A missing `last-version` counts as an upgrade from `unknown`. A downgrade is backed up like any other change.
- **Where.** `<R>/backups/<from>-to-<to>-<UTC time>/`, mode 0700. It mirrors `<R>`, with file modes kept, so restoring is a plain copy. It is built under a `.tmp-` name and renamed when complete. A start logs the path and the restore command.
- **What it holds.** The files marked "Yes" above. `audit.db` is copied with SQLite's `VACUUM INTO`, which includes rows still in the WAL file. Only regular files are copied: a symlink or any other type at one of these paths is skipped and logged, never followed.
- **What it leaves out.** `boxstate/`, `vm/`, `docker/`, `bundle/`, `broker/`, `ca/`, `clone-fetch/`, `analytics.db`, `.culibox-move`, runtime state, the transcripts and the Claude login inside accounts, symlinks inside an account's Claude folder (the account setup at start makes them again), and anything outside `<R>`.
- **Size bound.** An `audit.db` over 512 MB is not snapshotted: the log names its size and skips it. Restoring then keeps your current `audit.db`.
- **Retention.** After a backup completes, every backup from the last 30 days is kept, the 3 newest are always kept, and at most 20 are kept (the oldest go first). A folder in `backups/` whose name is not in the form above is never touched.
- **Fail closed.** If the backup cannot be made (disk full, a listed file that exists but cannot be read, `audit.db` that SQLite cannot read), `serve` refuses to start, before it changes anything else in `<R>`. The message names the cause and the file, points at `<binary>.prev` and at this page. `last-version` is not written, so the next start tries again. Fix the cause and start again, or put the previous binary back.

## Rolling back

1. **Stop CuliBox** (Ctrl-C, or stop its service).
2. **Put the previous binary back.** `scripts/setup.sh` keeps it as `culibox.prev` next to `culibox` and prints the line that restores it. `scripts/deploy-prod.sh` does the same for a deployed binary. Either script prints a `data:` line pointing here. Binary only: this does not undo data changes.
3. **Restore the snapshot,** to also undo what the newer build did to your data. `B` is the backup folder (the log line named it, or list `<R>/backups/`) and `R` is the data root:

```sh
R=~/.local/share/culibox
B="$R"/backups/<the folder you want>
rm -f "$R"/last-version && cp -a "$B"/. "$R"/ && rm -f "$R"/audit.db-wal "$R"/audit.db-shm
```

4. **Start the old binary.**

What to know before you run it:

- **Restore does not delete.** It copies the snapshot over `<R>` and leaves everything newer in place: accounts, store values, clone records and box homes made since the upgrade. `<R>` becomes the snapshot plus whatever is newer. Older builds ignore files they do not know.
- **It also restores `last-version`,** so the next upgrade takes a fresh backup.
- **Restoring `audit.db` drops the audit rows written since the upgrade.** To keep the current log, skip it and leave its `-wal` and `-shm` files alone:

```sh
rm -f "$R"/last-version && find "$B" -mindepth 1 -maxdepth 1 ! -name audit.db -exec cp -a {} "$R"/ \;
```

  Use this form too when the backup has no `audit.db` (it was over 512 MB). Whether an older build reads a newer `audit.db` is not tested yet.
- **Store changes revert too.** `store/` goes back to the snapshot, so a value you set, or a passphrase you changed (`culibox store passwd`), after the upgrade is gone. The old passphrase unlocks it again.
- **Restore into the same path.** On macOS the Keychain key is named by a hash of the data root path. Restoring into a different path leaves the key behind (the same rule as a data root move), and the store cannot be unlocked there. On Linux you unlock again with the passphrase.

## macOS

- The data root is on the Mac (`~/.local/share/culibox`), not inside the VM, so the backup and the restore work as on Linux.
- `vm-shares.json` and `inplace-locks.json` exist only on macOS. The backup includes them when present.
- The Keychain item is not copied. A restore into the same path keeps it valid. See "Restore into the same path" above.
- Security upgrades and the VM: see part D (#116). Until that is written, follow [macOS](MACOS.md), which says when `box setup -recreate` is needed.

## For contributors: the migration rule

A migration is any code that rewrites a persisted file when a new version first reads it. The rule is **copy**: the version-change backup is the copy from before the migration, so a migration does not need its own `.bak`. A new migration must:

- run only in `serve`, after the backup step;
- be idempotent;
- be crash-safe (write a temp file, then rename).

An older build reading what the migration wrote is **not** required to work. The exceptions are the files that a build must refuse to read when it is too old, rather than damage them (the version checks in the table above).

The data root move (the agent-box to culibox rename) runs before the backup by design: the backup holds the moved root, and its rewriters (the `config_dir` in `sessions.json`, the account `statusLine`, the `analytics.db` rows) have already run.

When you add a file to the data root, add it here with its writer, its format, how an older and a newer build treat it, and say whether the backup includes it. A file the backup should include goes in the list in `internal/upgradebackup`.
