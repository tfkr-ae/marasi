# Scope check prefixes a missing scheme with https

A scope check accepts a bare host the way the app tester does. A missing scheme, or a parse failure, is retried with `https://` prefixed. The result's tested URL is the URL the rules saw. A URL rule that looks for `http://` will not match a bare host.

**Considered options:** require a scheme and reject anything else. Rejected because the tester already accepts `example.com` and tells the operator that `https://` is used when the protocol is omitted.
