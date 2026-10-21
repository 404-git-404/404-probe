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
and the 700 KB of unrelated page content were removed.

## 送中 (sent to China) samples (R23)

Google decides per IP whether a visitor is treated as being in China. For such an IP the page's own
region (`INNERTUBE_CONTEXT_GL`, `contentRegion`) is only a default (US on all three hosts that were
checked, one of them in Hong Kong), and the page configuration carries a second ping URL on
`www.google.cn` in the top-level `ZWIEBACK_PING_URLS` key. Every such host also showed Google's
Gemini country marker `CHN` and a `google.com` redirect to `google.com.hk`, and the page contained
the message "YouTube Premium is not available in your country".

- `premium-cn-flagged-us-gl.html`: reduced from the real page of a flagged host. GL is US,
  `ZWIEBACK_PING_URLS` holds a google.com and a google.cn URL, and a reduced message block carries the
  Premium notice. Expected: cn, page region US, notice true.
- `premium-us-zwieback-clean.html`: the same shape from an unflagged host (`ZWIEBACK_PING_URLS` holds only
  the google.com URL). Expected: not_cn, US.
- `premium-synthetic-google-cn-outside-config.html`: **synthetic**. It was the v1.0.3 fixture; its
  google.cn line (`ytInitialData.adTracking`) was written by hand and is not the real page shape (the real
  URL is in `ZWIEBACK_PING_URLS`). v1.0.3 mislabelled that URL as an unrelated ad link. It now only tests
  that a google.cn string outside the strictly parsed configuration is never a verdict: expected unknown
  (`china_signal_unverified`).

## EU samples (v1.0.4)

`premium-de.html` and `premium-it-from-nl-host.html` have the same reduced shape as the pages above, with the
region taken from real responses of two EU test hosts (a German host: DE; a Dutch host: IT, which is Google's own
geolocation of that address). `consent-redirect-chain.txt` records the redirect chain every anonymous request from
the three EU hosts (DE, FR, NL) went through: `/premium` redirects to `consent.youtube.com`, which the Agent never
follows. Raw responses were deleted after the structure was extracted.
