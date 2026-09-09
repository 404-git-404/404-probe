# 404-probe v0.9.2 pre-release acceptance

Date: 2026-09-09 (America/Los_Angeles)

Scope: local Windows verification plus an isolated Debian 12 amd64 rehearsal on
the existing CloudCone test Server. The CloudCone primary service, its database,
Agents, Cloudflare configuration, the formal DediRock environment, tags, and
published releases were not changed.

## Candidate identity

The Linux rehearsal used an explicitly synthetic, unpublished candidate:

- version: `v0.9.2`
- commit fixture: `2222222222222222222222222222222222222222`
- Server SHA-256: `650e5c72392bfd818ebb15e5ba8d9a4a0a769ac455898024961c0f6dc8934418`
- installer SHA-256: `35103d72ca48ada63976212bd9b42e68739146997975d7c1197edb58e3b9cb3f`

The upgrade source was the public v0.9.1 amd64 Server with version identity
`v0.9.1`, commit `91a89cbaf2d21f64ed17340febd21eb80969661d`, and the
downloaded bytes were authenticated by the published v0.9.1 checksum manifest.
The synthetic candidate must not be published as a release asset.

## Local verification

- `go test ./...` passed after excluding the two selector tests already known to
  time out on the Windows host. All other packages passed.
- The complete unfiltered `internal/server` test binary passed on Linux,
  including both selector tests that time out on Windows.
- `bash -n` passed for the installer and both Server upgrade harnesses.
- The transaction fault harness passed on Windows Git Bash and native Linux.
  It covers corrupt and incomplete manifests, durable state failures, restoration
  of the previous helper or its absence, and strict recovery of a valid legacy
  v0.9.1 pending backup that predates `helper-state`.
- `git diff --check` passed.

## Domain and Web security verification

The native Linux Server suite passed all domain-policy and Web tests, including:

- exact-mode migration without implicit parent-domain trust;
- root, multi-level, and multiple-suffix admission with dot boundaries;
- strict Host parsing, effective-port canonicalization, and rejection of forged
  `Forwarded` and `X-Forwarded-*` headers;
- exact request Origin, CSRF, login-token, and host-only session isolation across
  allowed subdomains;
- refusal to expand the loopback-only insecure HTTP development exception to a
  suffix host;
- policy-generation invalidation so deleting and re-adding a suffix cannot
  revive an old login token or session;
- immediate policy changes, SSE closure, and new Agent commands using the
  current validated origin;
- selector-switch success, no-op, verification failure, stale choice, pause,
  revoke, and trust-boundary behavior.

The real helper menu was exercised under a pseudo-terminal. It remained open
after an invalid suffix and after refusing to remove the last suffix, then
successfully added, listed, removed, and disabled suffix mode. Exact-mode output
included the configured exact origin.

## Isolated systemd rehearsal

The rehearsal used only these dedicated resources:

- unit `404-probe-upgrade-rehearsal.service`;
- listen address `127.0.0.1:33444`;
- separate binary, configuration, database, transaction, and lock paths.

The following paths passed from schema 10 to schema 11 while preserving Agent
rows and database integrity:

- active and enabled upgrade;
- inactive and disabled upgrade;
- SIGKILL after binary replacement, conservative recovery to the exact v0.9.1
  binary/schema/helper, then a successful fresh retry;
- SIGKILL after helper replacement with restoration of the old helper, then a
  successful fresh retry;
- the same helper-replacement interruption when no helper existed before the
  upgrade, proving that absence is restored before a fresh retry.

All rehearsal resources were removed by the harness. Before and after the run,
the CloudCone primary Server remained active and enabled with unchanged PID
`319622`, listening only on `127.0.0.1:33333`.

## Remaining limits

- A real host power loss was not performed; SIGKILL is the durable interruption
  proxy.
- ARM64 was cross-built previously but was not run natively for this change.
- No public v0.9.2 URL or final release bytes exist yet.
- The public Cloudflare hostname still routes to the untouched primary service,
  so the new suffix behavior was validated against isolated Linux HTTP servers
  and the complete native test suite rather than by changing the Tunnel.
