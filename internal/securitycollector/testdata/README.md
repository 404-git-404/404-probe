# Security collector fixture provenance

`reality-overlapping-export.jsonl` is derived from the user-provided file
`已貼上文字 (1)(20260905-174954).txt` dated 2026-09-05. The source concatenates
three overlapping `journalctl`/query exports, so duplicate *complete source
lines* are removed only while building this test fixture. Production collection
does not deduplicate messages; it relies on verified journal cursors.

The deterministic converter is `scripts/convert-security-fixture.go`. It keeps
the event timestamp, source endpoint and REALITY error shape, removes prompts,
hostnames and process IDs, and creates fixture-only cursors from full-line
SHA-256 values. Source counts: 2,677 matching raw lines, 2,586 unique lines;
`64.83.31.13` has 2,573 unique events from 00:00:06 through 10:49:34 -0700.
The export is not a complete day and must not be described as network traffic
share.
