# Wordlist add moves the source file

Wordlists can be hundreds of megabytes, so the control listener does not take an upload body. Adding a wordlist takes a same-machine absolute path and moves that regular file into the machine wordlists directory under its basename. Copy would duplicate disk. Registering an external path would make a wordlist a pointer, which it is not. POSIX rename replaces an existing dest name, so add creates that name exclusively and leaves the source in place on conflict.

**Considered options:** copy into the wordlists directory; raw-body upload on the control listener; named pointer or symlink to a file elsewhere; `rename` that overwrites. Rejected because copy doubles disk for large lists, upload pumps those bytes through the control listener, a pointer contradicts the glossary, and replace would swap bytes under a name an Armory run may already be reading.
