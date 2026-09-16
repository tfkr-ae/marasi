# Artifact

Users attach files to a test case or finding in the open project, inspect metadata, download the bytes, and delete the attachment.

## Sub-features

- Upload with `artifact upload --file` and exactly one of `--test-case` or `--finding`.
- Read metadata with `artifact get`.
- Write the stored bytes with `artifact download`.
- Delete with `artifact delete`.
- Choose human-readable or `--json` output except on download.

## How to get to it (user POV)

Start a service first and create a test case or finding. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" artifact upload --test-case "$TEST_CASE_ID" --file proof.txt`. Copy the id into later get, download, and delete commands.

## Driving it with shell and curl

Run `artifact upload --test-case "$TEST_CASE_ID" --file "$PROOF_FILE" --json` and save `id`. Require `filename` to match the basename, `test_case_id` to equal `$TEST_CASE_ID`, and `finding_id` null. Run `artifact get "$ARTIFACT_ID" --json` and require the same id. Run `artifact download "$ARTIFACT_ID" --output "$DOWNLOADED_FILE"` and require the file bytes to match the source. Run `test-case get "$TEST_CASE_ID" --json` and require that id in `artifacts`. Run `artifact delete "$ARTIFACT_ID" --json` and require `{"id":"$ARTIFACT_ID"}`.

## Gotchas

- Upload requires `--file` and exactly one of `--test-case` or `--finding`.
- `--mime` is optional. When omitted, Marasi uses the file extension, or `application/octet-stream`.
- `artifact download` rejects `--json`. With no `--output`, it writes the filename in the current directory.
- The parent test case or finding must exist in the currently open project.
- After `project open`, previous artifact ids are not visible.
