# Recent traffic reference

Komari commit f18ad72c173b8821711cba0f2fb238c2486ec9af,
pkg/metric/raw_points.go, SHA256
4bebc4eda35cd6c14e452180e8cfdc97381f28420f54eda0b64c8559d08a469b.
https://github.com/komari-monitor/komari/blob/f18ad72c173b8821711cba0f2fb238c2486ec9af/pkg/metric/raw_points.go

Adapted synchronized memory-only samples, time pruning, private snapshots and
entity deletion patterns for two fixed rates. Fixed Go arrays replace the
general metric dictionaries/compression/rollup engine; no dependencies imported.
Original reference and MIT were actually read; accompanying license retained.
