# CLAUDE.md imports in boxed sessions

A boxed Claude gets your `CLAUDE.md` (see Config bundle above). If that file `@`-imports other files (`@docs/style.md`, `@~/notes/team.md`), a box that got only the `CLAUDE.md` would show those lines pointing at nothing. So CuliBox also gives the box the files it imports, read-only. This is on by default for a boxed profile that uses your personal config, and each profile can turn it off.

### What is followed

- Imports are found the way Claude Code finds them: a path relative to the importing file, `~/...`, or an absolute path under your home. Up to four hops are followed.
- An `@` inside a code span, a fenced code block or an HTML comment is not an import, and neither is a quoted path. A file that does not exist is skipped without a message.
- **Imports inside an imported file are followed only when that file is inside your `~/.claude` folder** (the config folder the bundle is built from). A file imported from anywhere else is still given to the box, but its own imports are not followed, and if it has any the profile editor lists it as left out. The reason: a box can rewrite files it can see, such as one in its workspace, and a new `@` line there would otherwise pull one of your secrets into the next box.

### Where files land in the box

- `~` maps to `/home/agent`, so `@~/notes/team.md` is at `/home/agent/notes/team.md`. An absolute path keeps its spelling.
- A relative import lands beside the file that imports it. From your `CLAUDE.md`, which the box has at `/home/agent/.claude/CLAUDE.md`, `@docs/style.md` lands at `/home/agent/.claude/docs/style.md`.
- A file the box can already see (the workspace, a `context_paths` entry, a clone share, `skills/`) is not mounted a second time. The box sees the live file there.
- CuliBox copies each file into a folder beside the bundle and mounts it read-only, one file at a time, so nothing else in that folder is exposed. The copy is what was hashed: `bundle.lock` has an `imports` list with each file's in-box path and sha256.

**Refusals** (the file is left out and the profile editor says why)

- A path that resolves, after symlinks, outside your home. A symlinked file must stay in the folder it is imported from.
- A path that goes through a folder symlink outside your `~/.claude` folder. A folder symlink on an import's path is followed only when the link itself is inside `~/.claude` (so `~/.claude/context` linked to `~/dotfiles/context` works). A box can replace a folder in its workspace with a link, and the import would then reach a file of the same name anywhere in your home. If you link a folder elsewhere, import it by its real path.
- Anything that is not a regular file: a directory, socket, named pipe or device.
- A file over 256 KiB, or more than 64 files or 4 MiB in total.
- A path the box provides itself: `/opt/agent-bundle` and anything under it, `/home/agent/.claude/CLAUDE.md`, `/home/agent/.claude.json`, `/home/agent/.claude/settings.json` and `/home/agent/.claude/.credentials.json`.
- Two different host files that map to the same in-box path.
- Anything inside CuliBox's own data, state and config folders, `~/.ssh`, `~/.docker`, `~/.colima`, `~/.lima`, `~/.aws`, `~/.config/gh`, `~/.kube`, `~/.gnupg`, `~/.netrc` or `~/.git-credentials`, and the config files the bundle rewrites or never copies (`~/.claude.json`, `settings.json`, `settings.local.json`, `.credentials.json`). These are checked by file identity, not by spelling, so a link or a different capitalisation does not get around them.

**Warnings** (the file is still given to the box)

- A file whose name looks like a secret (`secrets.zsh`, `*_key`, `*token*`, `*password*`, `.env*`, `*.pem` and the like), or one that has a line that looks like a credential (an API token, a private key header, `export NAME=value` for a secret-looking name). The editor names the file and line, never the value. The box can read and send what it is given: edit the file, or turn imports off.

### Turning it off

Set `imports_off: true` on the profile, or untick "Give the box the files my CLAUDE.md imports" under "What it can touch" in the profile editor. A profile with imports off and one with imports on never share a box. Host-tier profiles are not affected, and an agent account that does not use your personal config gets no imports.

### Lifecycle

- An edited import reaches a running box only when the box is recreated, like the rest of the bundle. A changed file gives the next spawn a new bundle.
- `bundle.lock` stays readable in the box even with imports off. It shows the paths and hashes of the files that would have been imported, not their contents, and lists the import spellings that were refused, with the reason.
- `context_paths` still refuses `~/.claude`; its error now points here.
- A custom `bundle_source` gets the same protection as `~/.claude`: no workspace, `context_paths` entry or clone share may be it or contain it, because the files in it have their own imports followed.
