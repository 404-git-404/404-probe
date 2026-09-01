# 404-probe V0.8 release notes

V0.8 adds an authenticated, observable, rollback-capable path for upgrading an Agent after one explicit local bootstrap. It does not create a release or tag by itself.

## Bootstrap and compatibility

- Existing V0.7 Agents must be upgraded locally with the version-pinned V0.8 installer. They cannot receive V0.8 through the Web UI.
- The bootstrap preserves the Agent identity, credential, Server URL, epoch state, and optional local Clash settings. It stages the candidate before stopping the old service and restores the previous binary if the new Agent cannot authenticate.
- The V0.8 Agent's first report uses the V0.7 shape. Version and updater capability fields are sent only after the Server advertises the corresponding report capabilities.
- Server schema V7 stores Agent version/capability and dedicated upgrade operations. Do not open a migrated database with an older Server.

## Remote upgrade flow

An administrator confirms one upgrade for one online, active, non-revoked, updater-capable Agent. The operation moves through `requested`, `claimed`, `downloading`, `verifying`, `staging`, `installing`, `restarting`, `health_check`, and a terminal `succeeded`, `failed`, or `rolled_back` state. Only one operation can be active per Agent.

The root updater listens on `/run/404-probe/agent-updater.sock`. The normal Agent remains unprivileged and can request only typed start, status, and healthy actions containing an operation ID and canonical target version. There is no shell or generic command surface.

Upgrade requests are rejected for offline, paused, revoked, or bootstrap-required Agents; non-release/dirty Server builds; invalid or non-newer versions; and concurrent operations. V0.8 has no automatic update, batch update, scheduling, reboot, custom download URL/path, downgrade, or Server self-update feature.

## Artifact trust and rollback

The updater supports Linux `amd64` and `arm64`. It derives a fixed official GitHub Release URL from the canonical version and uses HTTPS. A bounded, strict `RELEASE-METADATA.json` is the authoritative same-origin release identity: it binds the canonical version and commit to one fixed platform asset and digest. `SHA256SUMS` is retained as a packaging cross-check, and the metadata digest, checksum entry, and actual candidate digest must all agree. The updater statically reads Go build information to require the expected package, platform, clean state, and release commit. The root-owned candidate remains mode `0600` during verification and is never executed before installation.

The current binary is preserved, the verified candidate is installed with an atomic rename, and only the fixed `404-probe-agent.service` is restarted before health confirmation. Success requires a subsequent authenticated report that carries the target version. After that commit, the updater exits cleanly and systemd restarts it from the new live binary, so both the Agent and the privileged updater run the target build. A download, checksum, metadata, install, restart, or health timeout failure leaves the old binary in place or restores it.

`RELEASE-METADATA.json` is not independently signed: it, `SHA256SUMS`, and the artifacts come from the same trusted GitHub Release. The two manifests catch packaging and consistency errors but do not provide independent resistance to compromise of the official GitHub release account. A signing key and signed-manifest scheme remain future hardening.

## Verification before publishing

Run `go test ./...`, `go vet ./...`, validate `web/static/app.js`, and build Server and Agent for Linux `amd64` and `arm64`. The release asset script requires a clean worktree and exact matching tag, injects version and commit metadata, generates `RELEASE-METADATA.json` and `SHA256SUMS` from the same built assets, and verifies the checksums. Create the tag and release only after review.
