# Test case

Users record named test cases in the open project, attach captured traffic, and read a predefined checklist.

## Sub-features

- Create with `test-case create --title`.
- List newest first with `test-case list`.
- Read one case and its linked traffic with `test-case get`.
- Change title, description, category, tags, or note with `test-case update`.
- Delete with `test-case delete`.
- Attach an existing request UUID with `test-case link --request`.
- Remove a link with `test-case unlink --request`.
- Read the predefined checklist with `test-case checklist`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" test-case create --title "Auth bypass"`. Link a traffic id from `traffic list`. Read the bundled checklist with `test-case checklist`.

## Driving it with shell and curl

Run `test-case create --title "Auth bypass" --category Access --tag auth --note first --json` and save `id`. Run `test-case list --json` and require that id. Run `test-case update "$TEST_CASE_ID" --note replay --json`. Link a captured request with `test-case link "$TEST_CASE_ID" --request "$TRAFFIC_ID" --json`. Run `test-case get "$TEST_CASE_ID" --json` and require note `replay` plus that traffic id in `items`. Run `test-case checklist --json` and require a non-empty `title` and `items` whose members have `title` and `category` but no `id`. Unlink with `test-case unlink "$TEST_CASE_ID" --request "$TRAFFIC_ID" --json`, then `test-case delete "$TEST_CASE_ID" --json`.

## Gotchas

- `--title` is required on create and must be non-empty. Update requires at least one changed flag. Repeat `--tag` for multiple tags.
- Checklist items have no ids. The first `test-case checklist` writes `$VERIFY_CONFIG_DIR/test_cases.yml` from the bundled default if that file is missing.
- Duplicate link of the same request to the same case returns a conflict.
- Link, unlink, get, and delete look up ids in the currently open project. After `project open`, previous case and request ids are not visible.
- Human `test-case get` prints scalar fields only. Linked traffic (`items`) and `artifacts` appear in `--json`.
- `test-case get --json` includes an `artifacts` array filled by `artifact upload --test-case`.
