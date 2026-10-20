# Start command exits; the service instance keeps running

`marasi service start` used to be the service instance process, so the shell stayed blocked. The start command is a short-lived process that returns after the instance is running or startup fails. `service stop` already interrupts a running instance, and a second foreground mode would create two shutdown paths.

**Considered options:** keep start as the instance process; add `--foreground` beside detach. Rejected because the prompt never comes back in the first case, and two modes split signal handling and cleanup.
