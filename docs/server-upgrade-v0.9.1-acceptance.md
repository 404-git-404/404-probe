# Server upgrade v0.9.1 acceptance record

Date: 2026-09-08 (America/Los_Angeles)

This record covers the transactional Server upgrade work for v0.9.1. It is an
acceptance rehearsal, not a release record. No tag or GitHub release was created
or changed.

## Inputs

- Official v0.8.1 Server, linux/amd64
  - embedded commit: `4dfbd3e9018123d9e8a43a5857a5f0f6758abe35`
  - dirty: `false`
  - SHA-256: `a3d73fe7676909a86e984e118ca639cffe0e893e67c7949b9294b967f83024f3`
- Synthetic v0.9.1 Server rehearsal fixture, linux/amd64
  - embedded version: `v0.9.1`
  - embedded commit: `1111111111111111111111111111111111111111`
  - dirty: `false`
  - SHA-256: `6cd9db30ddd94838e80f954341554864a265dc39415223ea04a870553ef809c3`
- LF-only installer SHA-256:
  `bf1e3a5e97b39c83fd598560717d57ddae20ff5a804466971b2b8df1afb5e9e1`
- rehearsal `RELEASE-METADATA.json` SHA-256:
  `5f64498f6b8c8f3186e0891a7c9b0d07dddb8f85b61cc14c86ecf181a52f992a`
- rehearsal `SHA256SUMS` SHA-256:
  `ba63b781bbd0bb1c209baa289ccc98d4f6a0fe3f17f34caf21e2b4cf1925556c`
- systemd harness SHA-256:
  `da0a42f27a9738dc7d918a0ea9855638eaef773527ed24d3db8a1f8bb9e8b3a5`

The all-`1` commit is deliberately synthetic. These bytes are only a rehearsal
fixture and must not be published as the final v0.9.1 release.

## Local verification

The following checks passed on the working tree:

```text
go test ./cmd/server ./internal/buildinfo ./internal/releasemetadata ./internal/storage ./internal/updater -count=1
bash -n install.sh
bash -n scripts/build-release-assets.sh
bash -n scripts/test-server-upgrade-transaction.sh
bash -n scripts/test-server-upgrade-systemd.sh
scripts/test-release-installer-manifest.sh
scripts/test-server-upgrade-transaction.sh
git diff --check
```

The transaction harness covered absent WAL/SHM files, valid and corrupt backup
sets, duplicate and omitted manifest rows, state-sync failure, active and
inactive restoration, offline help, explicit-tag installer authentication and
forwarding, and primary/sidecar checksum failure.

A full `go test ./...` run is not green on this Windows host because two Web
selector end-to-end tests time out. The same two failures reproduce from an
unchanged HEAD archive, so they are recorded as an existing Windows/platform
baseline rather than accepted as v0.9.1 regressions.

## Isolated systemd acceptance

The rehearsal ran on the CloudCone Debian 12 amd64 test Server in the dedicated
unit `404-probe-upgrade-rehearsal.service`, bound to `127.0.0.1:33444`, with
separate binary, configuration, database, lock, and transaction paths. It did
not use the production unit, database, port, or Agent services.

The source database was created by the official v0.8.1 binary and contained
three representative Agent rows: two active and one revoked.

| Path | Old schema | Migrated schema | Rollback schema | Retry schema | Result |
| --- | ---: | ---: | ---: | ---: | --- |
| active + enabled | 9 | 10 | n/a | n/a | pass |
| inactive + disabled | 9 | 10 | n/a | n/a | pass; state preserved |
| SIGKILL after production migration and binary replacement | 9 | 10 | 9 | 10 | pass |

For the SIGKILL path, the interrupted installer left durable pending state. The
next invocation performed conservative recovery and intentionally stopped
instead of continuing the upgrade. Recovery restored schema 9, all three Agent
rows, active/enabled service state, and the exact old binary SHA-256
`a3d73fe7676909a86e984e118ca639cffe0e893e67c7949b9294b967f83024f3`.
A subsequent fresh invocation completed and produced schema 10.

Earlier isolated rehearsals also passed for v0.9.0 to the synthetic v0.9.1
candidate, including active/enabled, inactive/disabled, SIGKILL recovery, and a
Python SQLite online backup of the live test database with its exact three-Agent
listing preserved. Because v0.9.0 already uses schema 10, its migration,
rollback, and retry schema observations were all 10.

## Primary service and cleanup evidence

Before the cross-schema rehearsal, the primary test service was active and
enabled with PID `319622`, and its database listed three Agent rows. After all
success, inactive-state, crash, recovery, and retry paths, it was still active
and enabled with the same PID `319622`; the same three rows remained (two active,
one revoked).

The harness removed the isolated unit, enablement link, binary/config/database
trees, transaction backup, lock directory, and temporary transformed installers.
The upload directory `/tmp/404-probe-v091-cross-schema.DScvhx` was separately
verified as the intended root-owned, non-symlink path and removed. The local
generated fixture directory was also removed after recording its hashes.

## Remaining gaps

- A real power-loss or host-reset test was not performed; SIGKILL is the durable
  interruption proxy used here.
- Native ARM64 execution was not performed.
- A published v0.9.1 URL and its final release bytes do not exist yet, so the
  new-version public-download end-to-end path was not tested.
- The formal DediRock environment and Cloudflare configuration were not touched.
- The CloudCone primary service was deliberately not normalized or upgraded in
  place because its installed test commit is not an official release mapping.
