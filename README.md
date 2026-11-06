# sysprune

A safety-first, interactive terminal app for Linux (systemd-based distros) that helps you:

1. **Startup audit** — discover everything that starts automatically (systemd system & user units, XDG autostart entries, cron `@reboot` jobs) and walk through each item one at a time, deciding keep or disable, to cut idle resource usage.
2. **Storage audit** — reclaim disk space safely: caches, temp files, old downloads, large files, and duplicates.

Core principle: **safety over features.** Protected things are never offered for disabling, everything is reversible, and risky operations are previewed before anything runs.

![demo](https://img.shields.io/badge/platform-linux%20%2B%20systemd-blue)

## Install

Download the latest release tarball for your architecture, verify it, and drop the binary in `~/.local/bin`:

```sh
VERSION=X.Y.Z   # see the Releases page
ARCH=amd64      # or arm64

curl -LO "https://github.com/cmerk2021/sysprune/releases/download/v${VERSION}/sysprune_${VERSION}_linux_${ARCH}.tar.gz"
curl -LO "https://github.com/cmerk2021/sysprune/releases/download/v${VERSION}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt

mkdir -p ~/.local/bin
tar -xzf "sysprune_${VERSION}_linux_${ARCH}.tar.gz" -C ~/.local/bin sysprune
```

The binary is pure Go, statically linked (`CGO_ENABLED=0`), and needs no install step or root to run. Make sure `~/.local/bin` is on your `PATH`.

## Usage

```
sysprune                      # launch the TUI
sysprune --dry-run            # full audit + review flow; prints what WOULD change; applies nothing
sysprune --history            # non-interactive: list past sessions and changes
sysprune --restore [session]  # non-interactive: undo a session (default: most recent)
sysprune --reset-decisions    # clear "keep, don't ask again" memory
sysprune --version
```

`--history` and `--restore` work from a bare TTY with no TUI and no network — they are the recovery path if a desktop won't start after a change.

### The startup audit

- Choose a scope first: **User only** (default — nothing ever asks for root) or **User + System**.
- Items are reviewed one at a time, **leaves first**: things nothing else depends on come first. When disabling A leaves B with no remaining dependents, B is promoted with a note like *"Was only needed by A, which you just disabled."*
- Per item you can: **k**eep (or **K** keep and never ask again), **d**isable at next boot, **s** disable and stop now, **n** skip, **u**ndo last, **?** help, **q**uit. `m` (advanced, double-confirmed) masks a unit.
- "Disable and stop now" warns and names what is actively using the item (active dependents, triggering sockets/timers) and suggests handling those first. "Disable at next boot" always remains available.
- A final **review screen** lists all queued changes grouped by scope, re-validates the batch, and requires explicit confirmation. System-scope changes are labeled *will run as root*.

### The safety model (three layers)

1. **Protection list (hard lock).** A curated list of exact names and glob patterns (`dbus*`, `systemd-*`, `NetworkManager*`, `getty@*`, display managers, `polkit*`, `pipewire*`, `user@*`, firewalls, `sshd`, and more) that can **never** be queued for disable or mask from this app. They appear in a separate read-only "Protected" view with an explanation. Dynamic protections are added at runtime: the unit backing your current session, units required by `default.target`'s critical chain, providers of `network-online.target`, your display manager/compositor, and `sshd` whenever the session is over SSH.
2. **Dependency-based risk rating.** Everything else is rated 🟢 *Safe* (no active/enabled dependents) or 🟡 *Think twice* (has dependents, is pulled in by a boot target, or is socket/timer-activated), with the specific dependents shown. Queued disables count as already applied when rating later items.
3. **Knowledge base.** An embedded YAML file with 70+ entries gives friendly names, plain-language descriptions, and hardware-aware recommendations (e.g. *"No Bluetooth hardware found — bluetooth.service can be disabled"*). Unknown items fall back to layer 2 with a generic description.

### The privilege model

- The app **always runs as your normal user**. Reading state never needs root.
- In **User-only scope nothing ever escalates** — guaranteed.
- System-scope changes are queued into a JSON plan and applied by **one escalation** at the end: the binary re-invokes itself by **absolute path** (so sudo's `secure_path` can't miss it) via `sudo`, falling back to `pkexec`. You are shown exactly what will run before anything escalates.
- If neither sudo nor pkexec works, the exact commands are printed for you to run manually and the plan is saved to a file.
- The hidden `--apply-helper` mode is a **security boundary**: it re-validates everything independently (fixed operation allowlist, strict unit-name regex, plan file ownership/permission/size checks, no symlinks), re-checks the protection list, refuses protected units even in a hand-edited plan, and skips anything whose state drifted since review.

### Recovery

Every change is journaled **before** it is executed (write-ahead, append-only JSONL under `~/.local/state/sysprune/journal/`, mirrored from `/var/lib/sysprune/journal/` for system sessions).

```sh
sysprune --history            # list sessions: what changed, when, what failed
sysprune --restore            # undo the most recent session
sysprune --restore <id>       # undo a specific session
```

Restore re-enables units (restarting ones that were running), unmasks, restores moved XDG autostart files, removes `Hidden=true` overrides, reinstalls the backed-up crontab, and moves files back out of the Trash. Permanent deletions and cache cleans are listed as non-restorable. A crash mid-apply is detected at next startup and offered for reconciliation.

XDG autostart files are **never deleted**: user entries are moved to `~/.local/state/sysprune/backups/<session>/`; system entries get a `Hidden=true` override in `~/.config/autostart/` (no root needed).

### The storage audit

- **Regenerable** (safe to permanently delete, with path and size shown first): `~/.cache` per app, thumbnails, `/tmp`/`/var/tmp` old files, package manager caches, journal logs (vacuum), crash dumps, toolchain caches (pip/npm/cargo/go), Docker/Podman dangling data.
- **Personal files** (moved to **Trash** by default, with per-item opt-in permanent delete): largest files, duplicates, old downloads, empty files/broken symlinks — only inside folders you pick (XDG user dirs suggested).
- The duplicate finder groups by size → partial hash → full SHA-256, skips symlinks, treats hardlinks as one file, suggests a keeper (most permanent location, then oldest) and **never auto-selects** anything for deletion.
- The Trash follows the freedesktop spec (`~/.local/share/Trash`), so your desktop's trash can see and restore the files too. Emptying the Trash is an explicit, separate step with the size shown.
- Deletion paths are validated: resolved for symlinks, must stay inside the allowed root for their category, and `/`, `$HOME`, and shallow paths are always refused. `.git`, `node_modules`, `.venv` and similar are never scanned.

## Knowledge base file format

The embedded KB lives at `data/kb.yaml`. You can extend it with `~/.config/sysprune/kb.yaml`, merged over the embedded one. **User overrides may add protections and override entries, but can never remove embedded protection-list entries.**

```yaml
# Protection list: these can never be disabled from sysprune.
protected:
  - pattern: "myvpn*"            # exact name or glob
    reason: "My VPN must always run."
    # condition: ssh | firewall | timesync   (optional conditional protections)

# Knowledge entries: friendly info + recommendation.
entries:
  - match: "myapp.service"       # exact name or glob
    name: "My App agent"
    category: "apps"
    description: "Background agent for My App. Only needed if you use My App."
    recommend: think             # keep | disable | think
    reason: "Disable it if you stopped using My App."
    conditions:                  # optional, hardware/system-aware
      - check: dir_empty         # dir_empty | dir_missing | file_missing | file_exists | cmd_absent
        path: /sys/class/bluetooth
        then: disable
        note: "No Bluetooth hardware found."
```

If any condition matches, its `then` recommendation and `note` replace the defaults.

## Development

```sh
make build      # static binary ./sysprune
make test       # go test -race ./...
make vet lint   # go vet + staticcheck
make snapshot   # goreleaser release --snapshot --clean
```

All system interaction goes through a `Runner` interface (argv arrays, never a shell) and an `FS` interface, so the test suite runs entirely against fakes and never touches the real system.

### Cutting a release

```sh
git tag vX.Y.Z
git push --tags
```

The release workflow runs tests, then GoReleaser publishes `sysprune_<version>_linux_amd64.tar.gz`, `sysprune_<version>_linux_arm64.tar.gz`, and `checksums.txt` to the GitHub release. Tags with a pre-release suffix (e.g. `v1.2.0-rc1`) are marked pre-release automatically.

## Design decisions (where the spec allowed choices)

- **`sshd` is protected whenever the session is over SSH**, and the privileged helper *always* treats the session as SSH (maximum conservatism) — so a hand-edited plan can never disable sshd.
- **Firewalls** (`firewalld`/`ufw`/`nftables`) are protected by default; the advanced override only applies in the interactive UI with a heavy warning, never in the helper.
- **Static units** are shown informationally and cannot be queued (systemd does not support enabling/disabling them).
- **Alias unit entries** are skipped in discovery; the canonical unit is reviewed instead.
- The helper **ignores the user KB override file** entirely and validates against the embedded KB only, so a tampered user config cannot weaken the security boundary.
- **System-level storage operations** (journal vacuum, package caches, Docker prune) are routed through the same validated, journaled helper plan as unit changes, with a fixed operation allowlist and strictly validated parameters.
- Timer/socket-activated services warn on "stop now" because the trigger can wake them again; disabling the trigger first is suggested.
- `--restore` for system-scope sessions re-executes itself via `sudo`/`pkexec` by absolute path, same as apply.
- Crontab disabling comments lines out with a `# [disabled by sysprune]` marker after backing up the full crontab.

## Known limitations

- systemd only. OpenRC/runit/SysV systems are detected and refused at startup with a clear message.
- Resource figures (`MemoryCurrent`, `systemd-analyze blame`) are best-effort; some units report none and are shown without them.
- D-Bus client detection for "stop now" warnings is based on unit dependency/trigger data, not live bus introspection.
- Trash restore is only possible while the files are still in the Trash (an "Empty Trash" is permanent).
- The storage audit's system-level cleans free space measured by the respective tools; the preview sizes are estimates.
- Flatpak/snap cleanup covers unused runtimes/old revisions via the official tools; it does not remove applications.

## License

MIT — see [LICENSE](LICENSE).
