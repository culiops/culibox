# Reading other repos (context_paths)

`context_paths` lets a boxed agent read files outside its workspace: a partner repo's `CLAUDE.md`, a docs folder, a project registry. Each listed path is mounted **read-only at the same absolute path** inside the box, so a path written in your own notes resolves unchanged there. Boxed profiles only, in-place or clone mode.

The box can read everything you list and has internet egress: never list secrets.

| Field | Meaning |
|---|---|
| `context_paths` | Up to 16 absolute, clean paths to directories or regular files. Prefer a directory to a single file (see Lifecycle). Example: `["/home/me/other-repo/docs", "/home/me/registry"]`. The profile editor shows it as "Readable host paths", one path per line, for boxed profiles only. |

The box gets the list in `AGENTBOX_CONTEXT_PATHS`, `:`-separated, so the agent (or your user-level `CLAUDE.md`) can discover what is readable. CuliBox sets it itself: a value in the profile's `env` or the daemon's environment is dropped. As with a clone's `clone_share`, a listed `CLAUDE.md` is readable but **not** loaded as project instructions, because boxed Claude runs with `--setting-sources user`; tell the agent to read it.

**Refusals** (when you save the profile and when you spawn)

- A host-tier profile, including one with no runtime set while `default_runtime` is `host`: a host session can already read any path.
- More than 16 entries, a duplicate, or an entry inside another (list the outer one). A path that is not absolute or not clean, contains `:`, `,`, `"`, a control character or invalid UTF-8.
- `/`, your home directory or any directory above it, and paths that would shadow the box's own filesystem: `/etc`, `/usr`, `/var`, `/opt`, `/root`, `/run`, `/home/agent` and the other system directories, and anything inside them; and exactly `/home`, `/tmp`, `/mnt` and `/media`. A directory below those (`/tmp/x`, `/mnt/data/repo`) is fine, except CuliBox's own in-box paths `/tmp/agentbox-gh` (and anything inside it) and any `/tmp/ac-session-*`.
- A path that does not exist, has a symlink in any component, or is not a directory or regular file.
- A path equal to or inside `~/.colima`, `~/.lima`, `~/.docker` or `~/.ssh`; equal to or inside your personal Claude config directory `~/.claude` (its hooks and settings run in host sessions, so it is protected); and CuliBox's own data root, config and profile directories.
- A path inside a box-owned directory: a clone directory or one recorded in `<data dir>/clone-dirs` (see [Clone mode](CLONE-MODE.md)).
- A directory that contains a unix socket or a named pipe (FIFO) at any depth. A read-only mount does not stop the box from connecting to a socket or writing into a pipe: a Docker socket is root on the host, and a pipe feeds whatever host process reads it. The scan also refuses a directory with over 20000 entries or a subdirectory it cannot read, because either could hide one: list narrower subdirectories.
- A path that overlaps the profile's workspace (`default_cwd` or the clone directory) or `clone_of`, in either direction. Use `clone_share` to share part of a cloned repo.
- On macOS, a path outside the box VM's shared directories. Share its parent and run `agent-box box setup -recreate`.

**Warnings** (saved anyway; the save response and the profile editor show them)

- A path whose name looks like a secret or config file (`.env*`, `*.pem`, `*.key`, `config*.json`, `id_*`, `*.tfstate`, `*.tfstate.*`, `credentials*`, `*.tfvars`, `.netrc`, `.npmrc`, `.git-credentials`, `auth.json`, `*.p12`, `*.pfx`, `kubeconfig`).
- A directory with such files in it, scanned two levels deep (5000 entries, 20 names). `.gitignore` does not matter: the box reads whatever is on disk.
- A path that overlaps the workspace of another boxed in-place profile: that box can write what this one reads.

## Lifecycle

- The list is part of the box's identity. Changing it gives the profile a new box, with a fresh box home, rather than reusing the old one. Boxed sessions started in the old box cannot be resumed from the new one, and Claude's first-run onboarding may run again.
- You cannot add a path to a running box. List partner repos up front, then start a session that uses the new list.
- A listed file is mounted by inode. Git checkouts and most editors replace a file by renaming a new one over it, and a running (or reused) box keeps seeing the old one. List the folder that holds it instead where you can; a changed file inside a listed folder is seen at once. A later spawn into a box whose file (or a re-created folder) no longer matches the path is refused by the identity check below until that box is recreated.
- The paths are checked when you save, again at spawn before the box is prepared, and once more right before it starts. That last check records each path's identity (device and inode), and once the box runs, and before the agent starts, CuliBox compares it with what the box actually mounted. A mismatch means the path changed in between (for example a directory swapped for a symlink); the spawn is refused and a box created for it is removed. A box that other sessions still use is kept, and the refusal asks you to end those sessions so it is recreated.
  - On Linux the identity is read on the host, without following any symlink.
  - On macOS an inode number on the Mac is not the one the box sees through the VM's file share, so the identity is read inside the box VM instead: one `colima ssh` per spawn that has context paths, just before the box is created (for a box that is reused, just before the comparison). The same read refuses a path that has a symlink in any component, or is not a directory or regular file, at that moment. It needs the VM to be running, as any boxed spawn does; if it cannot be read, the spawn is refused.
- A socket or symlink that appears **after** the box starts is not caught: the mounts were made earlier and are not re-checked. Treat a listed directory as one that other tools on your machine can still change.
- Each spawn logs one line with the session id, the profile and the mounted paths.

## Known limits

- The comparison catches a path that changes after its identity was read, and nothing earlier. On Linux that read happens at the spawn's last check; on macOS it happens in the VM a little later, when the box is about to be created. A path swapped for another real directory before the read is taken as the path you listed (a symlink is refused, another real directory is not noticed). Do not list a path inside a directory another box can write.
- On macOS the VM's view of a path is what is compared. It was measured to match the box's mount on one setup (colima 0.8.1, virtiofs) and is not guaranteed on others; if it ever differed, every spawn with context paths would be refused, not let through.
