# Chrome config rereads YAML under a file lock

Chrome paths and chrome profiles live in the machine config file, shared by every service instance using that config dir. Every chrome control operation takes an exclusive file lock, rereads the YAML, then acts. Mutations write back under the same lock. That prevents torn reads of viper's in-place `WriteConfig` and lost updates when two instances add different entries. In-memory `Config` is a cache of the last locked read, not the source of truth. GET does not emit events after a reread. Events come from the instance that performed the mutation.

**Considered options:** in-memory only with no disk reread; GET rereads but mutations do not; unlocked reads; atomic temp-file rename so readers need no lock. Rejected because two instances would clobber lists, GET and start would disagree, viper writes in place, and rewriting viper is out of scope.
