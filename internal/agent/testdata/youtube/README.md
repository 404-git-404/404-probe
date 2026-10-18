# YouTube Premium page fixtures

These pages are reduced from real `https://www.youtube.com/premium` responses
fetched anonymously by three test hosts (Malaysia, United States, United
States). They keep the real inline-script shapes that the parser must handle:
the `ytcfg` stub with `window.ytcfg.set('KEY', 'value')`, the
`ytBootstrapConfig` IIFE that carries the region, trailing
`ytcfg.set("initialInnerWidth", window.innerWidth)` calls, the key/value
`ytcfg.set('initialBodyClientWidth', ...)` call and a single-quoted
`ytcfg.set({"CSI_SERVICE_NAME": 'youtube', ...})` call.

Sanitised: the configuration object is reduced to a few region/language keys;
visitor data, IP addresses, tokens, API keys, ids, nonces, the build version
and the 700 KB of unrelated page content were removed. `premium-us-b-...`
additionally keeps one unrelated `www.google.cn` ad-tracking URL, which an
older detector mistook for a China region.

## EU samples (v1.0.4)

`premium-de.html` and `premium-it-from-nl-host.html` have the same reduced shape as the pages above, with the
region taken from real responses of two EU test hosts (a German host: DE; a Dutch host: IT, which is Google's own
geolocation of that address). `consent-redirect-chain.txt` records the redirect chain every anonymous request from
the three EU hosts (DE, FR, NL) went through: `/premium` redirects to `consent.youtube.com`, which the Agent never
follows. Raw responses were deleted after the structure was extracted.
