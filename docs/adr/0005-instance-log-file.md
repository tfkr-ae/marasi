# Per-instance log file

After the start command exits, the service instance cannot keep writing to that terminal. Each instance appends to `<config-dir>/instances/<name>.log`, next to its socket and lock, with no rotation in this work. A failed start leaves the file in place. If the log file cannot be opened, start fails and no instance owns the name.

**Considered options:** inherit the terminal, syslog or the journal, discard. Rejected because the shell would not actually be free, the log path would not sit with the other instance files, and a later proxy-listener death would be undebuggable.
