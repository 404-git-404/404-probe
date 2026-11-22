# Security collector fixture provenance

`reality-overlapping-export.jsonl` is derived from the user-provided file
`已貼上文字 (1)(20260905-174954).txt` dated 2026-09-05. The source concatenates
three overlapping `journalctl`/query exports, so duplicate *complete source
lines* are removed only while building this test fixture. Production collection
does not deduplicate messages; it relies on verified journal cursors.

The deterministic converter is `scripts/convert-security-fixture.go`. It keeps
event timestamps, ports and REALITY error shapes, removes prompts, hostnames
and process IDs, and assigns fixture-only cursors in event order. It maps
distinct source addresses by first appearance to RFC 5737 or RFC 3849 ranges.
The source contained 2,677 matching raw lines and 2,586 unique lines.
The two synthetic sources account for 2,573 and 13 events respectively.
The larger count covers 00:00:06 through 10:49:34 -0700.
The export is not a complete day and must not be described as network traffic
share.
