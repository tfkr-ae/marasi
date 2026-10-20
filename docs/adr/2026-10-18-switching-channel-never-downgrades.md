# Switching channel never installs an older build on its own

When a user moves from nightly to release, Marasi keeps the nightly it has and offers an update only once a release is newer than that nightly's date. Going back to the latest release now is a separate, explicit reinstall that requires `--yes` (or `confirm` in the control API) and warns that projects opened in the nightly may not open in it. Marasi only migrates project files forward, so a nightly can leave a project in a schema the older release does not know. Builds compare by date, then release suffix; a release and a nightly of the same date are equal.

**Considered options:** install the latest release as soon as the channel changes; ask at switch time. Rejected because the first silently risks the user's projects and the second puts a schema question to someone who only wanted to stop getting nightlies.
