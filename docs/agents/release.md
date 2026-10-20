# Release

A release is an annotated `DD.MM.YYYY` tag on `main`. Pushing the tag publishes a public release, so get the user's go-ahead first.

1. On an up-to-date `main`, tag the commit with the release's theme as the message:

   ```bash
   git tag -a 19.10.2026 -m "Launchpad entry order and moves"
   ```

   `release.yml` triggers only on `DD.MM.YYYY` or `DD.MM.YYYY-<suffix>`. Its gate job fails a tag that is not on `main`.

2. Push the tag:

   ```bash
   git push origin 19.10.2026
   ```

3. Wait for the Release workflow (`gh run list --workflow=release.yml`). It tests, builds, signs, and publishes the release with generated notes, marked latest, titled with the bare tag.

4. Set the title to `<tag> - <Theme In Title Case>`:

   ```bash
   gh release edit 19.10.2026 --title "19.10.2026 - Launchpad Entry Order and Moves"
   ```

Nightly builds come from the separate Nightly workflow and are not part of this procedure.
