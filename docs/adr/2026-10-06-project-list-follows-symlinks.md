# Project list follows symlinks

A project is identified by its canonical absolute path. `marasi project list` includes a symlink in the projects directory, prints that canonical path, and uses the directory entry as the name. Two entries that canonicalize to one path are two rows. Wordlist and report template lists omit symlinks because those identities are filenames. A project is not.

**Considered options:** omit symlinks; collapse duplicate canonical paths to one row. Rejected because `project open --name` accepts the directory entry, and collapsing the rows would hide that name.
