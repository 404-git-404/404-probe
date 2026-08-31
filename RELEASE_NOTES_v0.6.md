# V0.6 release notes (draft)

This is a release-candidate draft. Do not publish it or create a V0.6 tag until every RC gate is complete.

## Agent lifecycle

- Pause and Resume temporarily stop and restore an Agent without changing its credential or deleting history.
- Revoke remains permanent. A revoked Agent exits cleanly, and the installed systemd unit's `Restart=on-failure` policy does not restart that intentional exit.
- Queued selector operations expire when an Agent is paused, disabled, or revoked. An already-running local operation may finish, but it is not executed twice.

## sing-box integration

- Agents can discover `Selector` state from a loopback sing-box Clash API and publish the latest allowlisted selector snapshot.
- The Web UI can request a switch between choices present in that snapshot.
- The Agent validates current local state immediately before mutation and performs a read-back afterward. A result is successful only when the local current choice matches the target; selecting the existing choice is an explicit no-op success.

## Safety boundaries

- The Clash API URL is restricted to loopback. Its optional Bearer secret remains in the Agent's private configuration and is not sent to the Server, Web UI, snapshot, or normal logs.
- Selector switching is a fixed domain Job. It does not add arbitrary shell, generic command, generic HTTP, configuration editing, restart/reload, bulk switching, scheduling, or automatic failover.
- Web mutations retain authenticated session, exact Origin, `Sec-Fetch-Site: same-origin`, and CSRF enforcement.

## Reliability and compatibility

- Selector operations expose stable queued, running, success, failed, and expired states, including changed/no-op results and structured local API failures.
- Stale or unavailable snapshots disable mutation, duplicate pending switches have deterministic rejection, and snapshot publication races use bounded recovery without fabricating a new current value.
- Schema V4 databases migrate through V5 to V6 while preserving credentials, Agent state, telemetry, and revoked records.
- V0.5 Agents remain compatible with the V0.6 Server report path during a rolling upgrade.
