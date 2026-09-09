# 404-probe v0.9.1

V0.9.1 adds an operator-triggered, transaction-safe Server upgrade path to the main installer command. It does not enable unattended updates and does not change the Agent Web updater authority.

## Server upgrade

Run the normal command on a supported existing Server:

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v0.9.1/install.sh | sudo PROBE_404_VERSION=v0.9.1 bash
```

This fixed-version command is available directly from this Release. The unified
`raw.githubusercontent.com/.../main/install.sh` entry will expose the same
upgrade flow only after the v0.9.1 installer is separately synchronized to the
main branch; this Release does not merge or otherwise change main.

The stable entry resolves only the fixed official GitHub repository and requires a canonical `vX.Y.Z` release. The release installer is authenticated by `SHA256SUMS`; the selected Server binary must agree with `SHA256SUMS`, `RELEASE-METADATA.json`, its embedded version and commit, platform, and clean-build state. An explicit `PROBE_404_VERSION=v0.9.1` on the privileged `bash` process pins a repeatable target. Resolution or verification failures do not fall back to an older default.

The installer recognizes complete Server and Agent layouts before changing live files. A supported Server shows its installed build and SHA-256, target release, database impact, and preserved state, then asks for one local confirmation. Same-version runs exit without replacement; known downgrades, modified builds, unknown legacy builds, mixed roles, unsafe paths, ambiguous units, non-loopback service endpoints, insufficient space, and unverifiable databases fail closed. Known pre-version-command Server releases are identified by statically inspected Go VCS metadata rather than executing an unsupported command repeatedly or guessing a version.

After preflight, the installer records the original active and enabled state in a root-only persistent transaction. It stops the unit, confirms no process still holds the database, and backs up the Server binary, configuration directory, unit, database, WAL, and SHM with ownership, modes, and timestamps preserved. It rehearses migration on a staging copy before marking production migration as started. The candidate replacement is a same-filesystem atomic rename.

For a previously active Server, acceptance requires the candidate unit to be active, its main PID to execute the installed candidate path, the running release identity to match the target, the authenticated Agent claim endpoint to return the expected response, and a separate read-only database schema/integrity query to pass. A previously inactive or disabled Server keeps that state; it is not silently enabled or made resident.

Failure and interruption recovery are phase-aware. If production migration may have begun, recovery stops the candidate, refuses incomplete backups, restores the pre-migration database set and old binary, and only then restores the old service state. The persistent marker covers process termination and power loss beyond shell traps. Recovery artifacts are retained under `/var/lib/404-probe-upgrade` with root-only access.

## Server diagnostic commands

The Server now provides `version --json` for reliable future upgrades. `database verify --db PATH` opens an existing database read-only and performs schema and SQLite integrity checks without migration. `database migrate-copy --db PATH` is the explicit staging-copy rehearsal operation; the protected production migration operation is separate and used only after the installer has persisted a complete stopped-service backup.

## Release packaging

The release builder exports `install.sh` from the tagged commit, rejects carriage returns, validates shell syntax, and includes the installer exactly once in `SHA256SUMS` alongside the four Linux binaries. Published v0.9.0 assets and its tag remain unchanged.

## Validation scope

The v0.8.1 schema-9 to v0.9.1 schema-10 path was exercised on an isolated Debian
12 amd64 systemd service, including active/enabled and inactive/disabled state,
an injected SIGKILL after production migration and binary replacement, exact
old-schema and old-binary restoration, and a successful fresh retry. Existing
test Server and Agent services were not upgraded in place.

The interruption test uses SIGKILL as the durable recovery proxy; a physical
power-loss or host-reset test was not performed. ARM64 release assets are
cross-compiled and metadata-verified but were not executed on native ARM64
hardware. Two Web selector end-to-end tests time out on the Windows development
host and reproduce unchanged from the pre-v0.9.1 HEAD archive; targeted Go,
transaction, release-manifest, and isolated Linux systemd acceptance tests pass.
