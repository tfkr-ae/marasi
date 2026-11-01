# Report

Users keep report templates for a running instance and export a rendered report from the open project's findings and test cases.

## Sub-features

- List templates by name and size with `report template list`.
- Move a source file into the templates directory with `report template add PATH`.
- Remove a template with `report template remove NAME`.
- Restore the embedded default with `report template restore`.
- Render a template to a file with `report export NAME --start YYYY-MM-DD --end YYYY-MM-DD`. Optional metadata flags are `--title`, `--client`, `--type`, `--scope`, `--assessor`, `--created-at`, and `--custom-properties`.
- Choose human-readable or `--json` output.

## How to get to it (user POV)

Start a service first. Run `dist/marasi --config-dir "$VERIFY_CONFIG_DIR" --instance "$VERIFY_INSTANCE" report template list`. Add, remove, restore, and export use the same global flags. Export also needs `--start` and `--end`.

## Driving it with shell and curl

Run `report template list --json` and require an item whose `name` is `default_template.md`. Require `$VERIFY_CONFIG_DIR/templates/default_template.md` to exist, and copy those bytes aside. Create a finding in the open project first. Write a source file outside that directory whose basename is `custom.md` and whose lines are `title={{.Metadata.Title}}`, `start={{.Metadata.Start.Format "2006-01-02"}}`, and `finding={{range .Findings}}{{.Title}}{{end}}`. Run `report template add "$SOURCE" --json` and require an item whose `name` is `custom.md`. Require the source path to be gone and `$VERIFY_CONFIG_DIR/templates/custom.md` to hold those bytes. Duplicate add of the same basename must fail, and that second source must remain. Run `report export custom.md --title Assessment --start 2026-01-02 --end 2026-01-16 --output "$OUT" --json` and require `path` equal to the absolute path of `$OUT`. Require `$OUT` to contain `title=Assessment`, `start=2026-01-02`, and that finding title. Add a second source whose basename is `props.md` and whose lines are `created={{.Metadata.CreatedAt.Format "2006-01-02T15:04:05Z07:00"}}`, `engagement={{.Metadata.CustomProperties.engagement}}`, `include={{.Metadata.CustomProperties.include_test_cases}}`, and `cases={{len .TestCases}}`, with one test case in the open project. Run `report export props.md --start 2026-01-02 --end 2026-01-16 --created-at 2026-01-16T17:30:00+05:45 --custom-properties '{"engagement":"Q1","include_test_cases":true}' --include-test-cases=false --output "$PROPS_OUT" --json` and require `created=2026-01-16T17:30:00+05:45`, `engagement=Q1`, `include=false`, and `cases=0`. Run `report export default_template.md --start 2026-01-02 --end 2026-01-16 --created-at 2026-01-16T17:30:00+05:45 --output "$ZONE_OUT" --json` and require `created: 2026-01-16 17:30:00` whatever the instance machine's zone. Run `report template remove custom.md --json` and require that file gone. Replace `default_template.md` with `edited`, run `report template restore --json`, and require the saved default bytes back. Run `report template remove default_template.md --json`, then `report template restore --json`, and require those bytes again. Run `report export default_template.md --title Assessment --start 2026-01-02 --end 2026-01-16 --output "$DEFAULT_OUT" --json` and require that file to contain `title: Assessment`, `# Executive Summary`, and that finding title.

## Gotchas

- `report template add` moves the source file, including when the source is on another drive. The path must be a regular file outside the templates directory. The stored name is the basename. Add does not check template syntax. A duplicate basename fails with `adding report template: report_template_already_exists` and leaves the new source in place.
- The default template is `$VERIFY_CONFIG_DIR/templates/default_template.md`. A doctor-checked start has already installed it. Templates live under the config directory, not the open project. `project open` does not hide them. Findings and test cases in an export do follow the open project.
- List is filename order. It omits hidden files, directories, and symlinks. It does not put `default_template.md` first.
- Remove may delete `default_template.md`. Restore overwrites an edited default and recreates a missing one. Other templates are left alone.
- `--start` and `--end` are required and must be `YYYY-MM-DD`. `--draft` defaults true. `--include-test-cases` defaults true. `--truncate` defaults `0`. `--title`, `--client`, `--type`, `--scope`, and `--assessor` are written into the template metadata. CLI flags are `--draft`, `--truncate`, `--include-test-cases`, `--title`, `--client`, `--type`, `--scope`, `--assessor`, `--created-at`, and `--custom-properties`, not the JSON names `is_draft`, `truncate_length`, `include_test_cases`, `created_at`, and `custom_properties`.
- `--created-at` must be RFC3339 with an offset (`2026-01-16T17:30:00+05:45` or `…Z`); a bare date fails before any request. Its offset is kept, so `.Metadata.CreatedAt` prints in that zone. Without it, the instance stamps the export time in its own machine's zone, which over `--remote` may not be the user's.
- `--custom-properties` must be a JSON object. Without it `.Metadata.CustomProperties` is `{}`. A template that reads a missing key fails the export (`missingkey=error`). If the object holds `include_test_cases`, the instance replaces that value with `--include-test-cases` (default true), which also decides whether test cases are listed.
- Export writes the rendered bytes to a file. Human stdout is that absolute path. `--json` prints `{"path":...}`, not the report. With no `--output`, the name is `--title` plus the template extension when the title does not already end with that extension, or the template basename when `--title` is empty, in the current directory. A failed export does not replace an existing output file.
- Add, remove, and restore publish `report.template.added`, `report.template.removed`, and `report.template.restored`. List and export do not. Subscribe with `events` before the mutation; the stream has no replay.
- Human add, remove, and restore print a confirmation to stderr. `--json` add, remove, and restore print the full list.
