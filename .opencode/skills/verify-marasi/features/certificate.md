# CA certificate

Users fetch the running instance's CA certificate for trust installation or HTTPS interception diagnostics.

## Sub-features

- Read the loaded CA certificate with `certificate get`.
- Choose PEM text with `--format pem` or DER bytes with `--format der`.
- Choose human-readable PEM or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" certificate get`. Use `--format der` when the consumer needs raw bytes.

## Driving it with shell and curl

Run `certificate get --json` and require a non-empty `pem` starting with `-----BEGIN CERTIFICATE-----`, plus `subject`, `issuer`, `not_before`, `not_after`, and `spki_hash`. Run `certificate get --format pem` and require the same PEM header on stdout. Run `certificate get --format der` and require non-empty bytes that decode as the PEM block. `--format` with `--json` fails locally before contacting the service.

## Gotchas

- `certificate` requires the `get` subcommand.
- `--format` must be `pem` or `der` and defaults to `pem`.
- `--json` returns the certificate envelope. Human `pem` prints only the PEM block. Human `der` writes raw certificate bytes.
- The certificate belongs to the running instance. Restarting the service can rotate it.
