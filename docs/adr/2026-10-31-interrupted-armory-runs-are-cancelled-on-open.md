# Interrupted Armory runs are cancelled on open

An Armory run is `in_progress` only while the instance that started it executes it. When that instance stops mid-run, by a crash, a kill, or power loss, the stored run stays `in_progress`, and start (`not_a_draft`) and cancel (`run_not_active`) both refuse it for good. Opening a project therefore sets every `in_progress` run to `cancelled` with `finished_at` now, in one statement, before the Armory manager exists. The project lock is held by then, and opening the path that is already open returns early, so no live run is ever reset. No event is published: the project is not published yet, and clients re-read on `project.opened`. Marasi App v1 made the same repair on every open.

**Considered options:** `failed` with a reason. Rejected because v1 already records these runs as `cancelled`, an Armory run has no reason field to carry the cause, and the run did not fail on its own: something outside it stopped it, as a user's cancel does.
