The page scripts of v1.0.6, byte for byte (`network-quality.js`, `network-quality-core.js`, `latency-mini.js`). They are what a browser tab
that was opened before a Server upgrade still runs. `scripts/test-v107-old-page.cjs` runs them against the retired routes (410) to show that the
old page asks for a refresh instead of breaking. Do not edit.
