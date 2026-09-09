# 404-probe v0.9.2

V0.9.2 introduces hot, persistent management of allowed Web login domain
suffixes while retaining exact per-request Origin and CSRF enforcement.

Existing v0.9.1 installations migrate into exact-origin mode with no expansion
of trusted hosts. Administrators explicitly enable suffix mode through the
offline local `404-probe-install domains` menu or its `list`, `add`, `remove`,
and `disable` subcommands. Multiple registrable root domains are supported;
removing the final suffix is rejected, and disabling suffix mode requires local
confirmation.

Host parsing rejects ambiguous or injectable targets. Domain admission uses the
actual request Host and a locally configured scheme, ignores untrusted forwarded
headers, and uses DNS-label boundaries. Login tokens and sessions are bound to
the canonical request origin. A suffix permits new logins at its subdomains but
does not permit cross-subdomain Origin or CSRF requests. New Agent install
commands use the current validated request origin; existing Agent configuration
is not rewritten.

Every actual domain-policy add, remove, or disable change advances the policy
revision and invalidates all existing Web sessions and login tokens, requiring a
new login. No-op requests such as adding an already configured suffix do not
advance the revision or invalidate credentials.

The Server upgrade transaction also refreshes the installed local helper. The
old helper or its absence is included in the durable backup and restored after a
failed or interrupted upgrade.

Validation includes native Linux Web tests and isolated Debian systemd upgrade
and interruption recovery. See `docs/server-upgrade-v0.9.2-acceptance.md` for the
acceptance scope and remaining platform limits.
