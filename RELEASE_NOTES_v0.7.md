# 404-probe v0.7.0

V0.7 tightens Web recovery and Agent lifecycle semantics while preserving the V0.6 protocol, schema, credentials, and Server-side history.

## Login recovery

- A login form issued before a Server restart no longer leaves the browser on a dead JSON error page.
- An expired or unknown login request returns a fresh, usable login form with a clear sign-in-again message.
- Exact Origin, `SameSite=Strict`, CSRF token, bounded form, rate-limit, and password-verification boundaries remain unchanged.

## PAUSED telemetry semantics

- Pausing an Agent immediately presents current RX/TX rates as zero.
- CPU, RAM, Swap, Disk, and Load display as unavailable while the latest sample is stale.
- After Resume, stale/zero semantics remain until the first new report arrives; that report restores live metrics without carrying paused state forward.

## Agent identity UX

- Ordinary Dashboard, enrollment, revoke, history, Schedule, and Probe Job views no longer display or offer a copy action for Agent IDs.
- Agent IDs remain unchanged in authenticated APIs, URLs, storage, protocol messages, and internal correlation paths.

## Explicit Agent uninstall

- `404-probe-install uninstall agent` now removes the Agent systemd unit, binary, private environment file, and epoch/state files.
- The command is safe to repeat after a complete or partial cleanup.
- Systemd journal history, the locked service account, installer helper, and all Server-side telemetry remain preserved.
- Revoke remains a credential action and never performs remote filesystem deletion or uninstall.

## Compatibility and upgrade notes

- V0.7 continues to use schema V6; V0.6 databases require no schema migration.
- V0.6 Agents remain compatible with the V0.7 Server during a rolling upgrade.
- Back up the Server database, upgrade the Server first, and then upgrade Agents individually.
- Do not replace Revoke with uninstall: revoke the credential on the Server when permanent credential invalidation is required, and run uninstall explicitly on the Agent host when local software removal is required.
