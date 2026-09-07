# 404-probe V0.9 release notes

V0.9 adds bounded, local-first Security Observability for invalid sing-box REALITY connections and cleans up Agent installation and dashboard behavior. It does not add firewall management, automatic bans, remote shell access, arbitrary systemd unit selection, regular-expression jobs, realtime journal streaming, notifications, or Server self-update.

## Security collection boundary

- `404-probe-agent security-collect` accepts no arguments and runs only from a root systemd oneshot. It invokes the fixed `/usr/bin/journalctl` binary for `_SYSTEMD_UNIT=sing-box.service`, with JSON output, a saved cursor, and fixed time/size limits. It has no network access or credentials.
- Root-private cursor, pending transaction, and outbox files use mode `0600` below mode `0700` directories. Aggregate exports use `root:404-probe` mode `0640`; the Agent service gets read-only access.
- Writes are staged and atomically renamed. Recovery commits a stable pending batch before reading more journal data. Cursor advancement, outbox/export publication, and retention pruning are ordered to avoid presenting uncommitted progress.
- One run accepts at most 100,000 lines, 64 MiB, 90 seconds, and 4,096 unique IPs. The upload contains at most the top 100 deterministically sorted sources and the Agent endpoint accepts at most 128 KiB.
- Raw journal records, ports, hostnames, process identifiers, and malformed text are not uploaded. Only canonical IP addresses, counts, first/last timestamps, duration, and typed classifications are exported.

Classifications are deterministic: `OBSERVED` for fewer than 5 events, `REPEATED` from 5 events, `PERSISTENT` from 20 events plus at least one hour and six distinct five-minute buckets, and `HIGH_VOLUME` from 1,000 events plus at least one five-minute bucket containing 100 events. `PERSISTENT` and `HIGH_VOLUME` may coexist.

Completeness is explicit. Invalid or missing cursors, oversized input, malformed required fields, dropped sources/events, journal failures, and outbox delivery gaps cannot be labelled complete. Delivery coverage metadata is stored separately from the immutable batch hash, so retrying a batch remains idempotent and later discovery of a transport gap does not create a content conflict.

## Protocol and storage

The Agent advertises Security support only after the Server negotiates it in the normal report response. `/api/v1/agent/security` authenticates the same Agent credential, accepts strict bounded JSON, and fences submissions to the current Agent epoch and session. Duplicate `batch_id` plus identical content is idempotent; the same ID with different content is rejected. Older batches are retained as history but cannot regress the current status. Security history is deleted after 30 days.

Schema V10 adds `agent_security_capabilities` and `agent_security_batches`. Back up the SQLite database before migration and do not downgrade afterward.

## Installation and upgrades

Fresh V0.9 Agent installs create and start the Security oneshot/timer and record the read-only export paths. Existing V0.8 Agents should first use the Web binary upgrade, then run locally:

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v0.9.0/install.sh | sudo bash -s -- setup-security
```

The command requires root and the exact verified V0.9.0 Agent build. It validates existing ownership/modes, preserves identity, credentials, Server URL, and epoch, installs only fixed Security units/configuration, verifies a real export is readable but not writable by the Agent account, and rolls back changes on failure. It does not accept a remote unit, command, journal filter, URL, or token.

The Server-generated fresh install command is available only when its own build has a canonical version and commit. Development, dirty, or unknown Server builds no longer silently generate an installer for an older release.

## Dashboard

Agent cards no longer use a hard-coded height or fixed row layout, preventing Security and Google status rows from overlapping. YouTube routing retains the typed backend state while the visible China label is simply `CN`.

Selector drafts are keyed by Agent and selector and survive authenticated SSE rerenders. A successful switch clears the draft; a failed switch retains it; if the option disappears from a refreshed allowlist, the draft is discarded with visible feedback.

## Verification note

The Security helper and systemd sandbox must be exercised on Linux with systemd and a readable journal. Windows can run protocol, storage, HTTP, Agent, Web, installer-contract, and fixture tests, but the existing real-collector Server integration tests require Linux `/proc/sys/kernel/random/boot_id` and time out on Windows.
