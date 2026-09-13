# 404-probe v0.9.3

V0.9.3 is a dashboard and Linux telemetry release with compatibility-gated
report fields, locally bundled country/region presentation, and safer selector
state publication.

## Dashboard

- Reworks the overview into compact warm frosted cards, with four columns on
  desktop and one on mobile. Green, orange, and red states make healthy,
  attention, and failure conditions immediately distinguishable.
- Keeps identity and availability prominent while showing RAM and disk as
  used/total values, current network rates, plan price and billing period,
  traffic allowance/usage, and manual traffic calibration context.
- Retains the four Google/YouTube status checks on each overview card below the
  current-cycle traffic summary, while keeping maintenance controls secondary.
- Removes client-side card pagination. The All Agents view is one continuous,
  scrollable list.
- Keeps expanded details, the plan editor, keyboard focus, and scroll position
  stable across the 15-second refresh and Server-Sent Events updates.
- Shows a bundled local SVG country/region flag beside each Agent name. The
  MIT-licensed flag assets are vendored from `flag-icons` 7.5.0; no runtime
  image request leaves the Server. A manual
  uppercase ISO alpha-2 override wins over the Agent-reported value and can be
  cleared to return to the installation-time cached value. Unknown values use a
  neutral globe and clearing an override never triggers another lookup.
- Displays sing-box selectors in the top-level configuration order when the
  protected root-generated metadata is available, with deterministic name
  order as the explicit fallback.

## Agent egress country/region

- Adds optional country code, CPU core count, CPU steal, and disk I/O report
  fields behind explicit Server capability negotiation. A new Agent omits all
  of them until the Server declares support, so strict v0.9.2 report decoding
  continues to accept the first and subsequent reports. Country data also has
  schema storage, Control/Web API views, validation, and manual override
  persistence.
- Accepts only ISO alpha-2 entries represented by the bundled local flag set;
  syntactically shaped pseudo-codes such as `AA` and `ZZ` are rejected before
  storage or display.
- A fresh Agent installation performs exactly one bounded HTTPS request to
  `https://ipwho.is/?fields=success,country_code`. The request sends no Agent
  credential, hostname, or telemetry; the provider necessarily observes the
  public source IP and request time. Only the validated country code is written
  to the private Agent environment. Failure is non-fatal and remains unknown.
- The lookup client ignores `HTTP_PROXY`/`HTTPS_PROXY`, sends no cookies or
  authentication, rejects redirects, limits the response to 1 KiB, and times
  out after four seconds. A system-level transparent proxy, VPN, or TUN can
  still affect the observed egress; the result is not a physical-location
  guarantee and can be corrected manually.
- Restarts, upgrades, and existing-Agent bootstrap do not query. IP changes or
  recognition errors are handled with the existing Server-side manual override.
- The value represents only the Agent host's own public egress. It must never be
  inferred from the browser, Server, Tunnel, YouTube, or other service-region
  results.

## Linux telemetry

- Adds optional CPU steal, aggregate disk read/write rate, and most-busy logical
  disk percentage, with independent baselines and topology-aware device
  selection for partitions, device mapper/LVM, crypt, MD, and nested holders.

## Reliability and compatibility

- Serializes periodic selector discovery with selector switching so an older
  in-flight snapshot cannot overwrite the verified post-switch state.
- Initializes SQLite foreign-key enforcement and the busy timeout on every
  physical connection, including replacements created by the connection pool.
- Preserves strict older-Agent compatibility by negotiating every new report
  field before it is sent.
