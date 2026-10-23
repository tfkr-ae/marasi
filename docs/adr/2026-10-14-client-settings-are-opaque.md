# Client settings are opaque to Marasi

A project keeps a JSON value per client name and key for clients such as marasi-app, which store their ledger settings there (marasi-app ADR 0004). Marasi validates only the shape: the client name, the key, and that the value is JSON. It never reads a value's meaning, and each key is written on its own, so two writers changing different keys cannot overwrite each other.

The content-type exclusion that Marasi stored in `app.filters` but never applied moves into this store under the client name `marasi`, which clients cannot write. A migration copies it to `legacy.content_type_exclusion` only when it differs from the seeded default, then drops the column. A missing key therefore means the project never changed the default. One client migrates it into its own key and deletes the `marasi` key. Marasi does not copy it under a client's key, because Marasi would then hard-code that client's key and value format.

Marasi reads the settings it keeps under `marasi` itself, such as the project's upstream proxy selection ([Upstream proxies are listed per machine and selected per project](2026-10-23-upstream-proxies-are-listed-per-machine-and-selected-per-project.md)).

**Considered options:** typed Marasi tables for saved queries and labels; a read-only legacy accessor kept for one release. Rejected because typed tables add Marasi surface that only one client uses, and an accessor is API that must be removed later.
