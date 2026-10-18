# Service instances install updates, and an install outlives its client

The installed build is replaced by a service instance through `POST /update/install`, not by the CLI process. `marasi update install` is a client like any other, so a GUI that only talks to the service can update Marasi the same way. Updating therefore needs a running instance; with none, the user starts one or downloads the release by hand. The instance replaces its own executable (symlinks resolved) and reports the path. An install directory it cannot write to is refused before anything downloads; Marasi never asks for elevation.

An install reports its steps (download, verify, install) as events and through `GET /update`, and it continues when the client that started it disconnects. This deliberately departs from ADR-0026: half an install is worse than a finished one, and a second client must be able to watch an install it did not start. `DELETE /update/install` cancels it until the swap begins. One install runs per installed build: a second request on the same instance joins it, and a request on another instance gets `409` naming the instance that holds it.

Running instances keep the code they started with and report `restart required` until they restart (ADR-0051).

**Considered options:** the CLI replaces the installed build locally and the service only reports availability; shared logic that both the CLI and the service run. Rejected because service-only GUIs could not install, and two install paths would drift on locking and verification.
