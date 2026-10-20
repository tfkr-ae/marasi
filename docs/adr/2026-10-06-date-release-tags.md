# Release tags are DD.MM.YYYY

Production git tags, `make`'s `VERSION`, and `GET /service/status` are the same string: `DD.MM.YYYY`, UTC date of the cut. A second production cut the same day is `DD.MM.YYYY-2`, which already exists (`23.06.2026-2`). A local `make build` with no override reports `DD.MM.YYYY (dev)`.

Nightly is the exception. The git tag is a moving `nightly`. Status reports `DD.MM.YYYY (nightly <shortsha>)`, same parenthetical shape as `(dev)`, so two runs the same day still differ. Nightly does not use the `DD.MM.YYYY` tag prefix.

**Considered options:** SemVer (`v1.0.0`); ISO CalVer with a `v` prefix (`vYYYY.MM.DD`); nightly status equal to the `nightly` tag; nightly status as `DD.MM.YYYY-nightly` with no SHA. Rejected because tags in this repo are already `DD.MM.YYYY`, the Makefile default is `DD.MM.YYYY (dev)`, a third scheme would make production tag and status disagree, and a date with no SHA cannot tell two nightlies apart.
