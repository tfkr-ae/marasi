# Service list keeps an unreadable status

`marasi service list` dials each control socket in the selected config dir. A socket that never accepts a connection is omitted. A connection that does not return the status document is still a row: `status` is `unhealthy`, and the row names the instance. The command exits 0. A success row stays the status document, including `status` `running`. Failing the command, or omitting the broken instance, would hide either the instances that answered or the one that did not.

**Considered options:** fail the whole command; omit the broken instance; rename `running` to `healthy` in the list. Rejected because a failed command prints no rows, an omitted instance looks stopped, and `service status` already says `running`.
