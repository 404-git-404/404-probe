# Server upgrade fault-injection plan

Run these cases first against an isolated Debian systemd service and a copy of a real test database. Record the original binary/config/unit/database hashes, owners and modes, Server active/enabled state, Agent count and IDs, and connected-Agent recovery time. Never point the harness at the formal DediRock service.

For each failure point, test both an originally active+enabled Server and an inactive+disabled Server. After failure and again after rerunning the installer, assert the expected binary version, database schema/integrity, absence of mixed old-binary/new-database startup, original service state, unchanged credentials/configuration/listen origin, and retained root-only recovery material.

| Injection point | Expected durable phase | Expected result |
| --- | --- | --- |
| Latest release resolution, installer checksum, metadata, candidate checksum/build identity, read-only DB verification, or disk-space preflight fails | none | Live service and files remain untouched; candidate staging is removed. |
| Operator declines confirmation | none | Live service and files remain untouched. |
| Stop command fails or process remains active | `prepared` | Recovery restores the original active/enabled state; no backup or migration is claimed. |
| SIGTERM or power loss immediately after stop | `prepared` or `stopped-unbacked` | Rerun restores original state before allowing a fresh attempt. |
| Another process holds DB/WAL/SHM | `stopped-unbacked` | No inconsistent backup is accepted; old binary/database restart only in the original state. |
| Binary, config, unit, DB/WAL/SHM copy or fsync fails | `stopped-unbacked` | Incomplete backup is never marked complete; unchanged live data can restart with the old binary. |
| Staging-copy migration/integrity check fails | `backup-complete` | Production DB and binary are unchanged; original service state returns. |
| SIGKILL during production migration | `migration-started` | Rerun requires a complete backup, restores the full pre-migration DB set and old binary, then restores service state. |
| Atomic binary rename is interrupted | `migration-started` or `binary-replaced` | Either state restores old DB+binary; no old binary opens the migrated DB. |
| Candidate start, readiness, PID/executable, version, HTTP, or DB query fails | `binary-replaced` or `candidate-started` | Candidate is stopped; pre-migration DB and old binary are restored before any old start. |
| Recovery backup is missing or damaged | any phase requiring backup | Fail closed with Server stopped; keep marker and all recovery files for manual intervention. |
| Successful upgrade is interrupted just before marker removal | `candidate-started` | Conservative rerun rolls back completely; a later fresh run can upgrade again. |
| Successful upgrade completes | none | Target binary and healthy DB are active only if originally active; enabled state matches original; protected backup remains. |

Also exercise same-version, downgrade, unknown legacy commit, modified build, unsafe symlink/path, mixed Server+Agent layout, non-loopback/ambiguous unit, insufficient space, corrupt DB, newer unsupported schema, absent WAL/SHM, large WAL, and two concurrent installer processes. Validate the published main-URL stdin command separately after release/main synchronization, including LF-only bytes, recursion prevention, canonical target pinning, and failure without fallback when latest resolution or asset verification is unavailable.
