# Finding

Users record findings in the open project, optionally point them at a test case, and attach captured traffic as evidence.

## Sub-features

- Create with `finding create --title`.
- List newest first with `finding list`.
- Read one finding and its linked traffic with `finding get`.
- Change title, severity, CVSS fields, writeup, treatment plan, or related test case with `finding update`.
- Delete with `finding delete`.
- Attach an existing request UUID with `finding link --request`.
- Remove a link with `finding unlink --request`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" finding create --title "Broken access control"`. Pass `--test-case` with a test case id when the finding belongs to one. Link a traffic id from `traffic list`.

## Driving it with shell and curl

Run `finding create --title "Broken access control" --severity High --test-case "$TEST_CASE_ID" --json` and save `id`. Run `finding list --json` and require that id. Run `finding update "$FINDING_ID" --severity Medium --json`. Link a captured request with `finding link "$FINDING_ID" --request "$TRAFFIC_ID" --json`. Run `finding get "$FINDING_ID" --json` and require `severity` `Medium`, `test_case_id` equal to `$TEST_CASE_ID`, and that traffic id in `items`. Unlink with `finding unlink "$FINDING_ID" --request "$TRAFFIC_ID" --json`, then `finding delete "$FINDING_ID" --json`.

## Gotchas

- `--title` is required on create and must be non-empty. Update requires at least one changed flag.
- `--severity` is `Critical`, `High`, `Medium`, `Low`, `Informational`, or empty. `--test-case` and `--clear-test-case` cannot be used together on update.
- A related test case id must already exist in the open project.
- Duplicate link of the same request to the same finding returns a conflict.
- Link, unlink, get, and delete look up ids in the currently open project. After `project open`, previous finding and request ids are not visible.
