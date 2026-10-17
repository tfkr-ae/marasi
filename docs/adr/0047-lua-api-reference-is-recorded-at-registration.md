# The Lua API reference is recorded where each function is registered

An agent that writes extension Lua needs to know the hooks, libraries, types, and methods the installed version provides. `marasi extension api [NAME]` prints that reference, and the agent skill points to it instead of listing functions. The command needs no instance, like `marasi skill`, because the reference describes the binary, not a project.

Each Lua function and method is registered as an `APIFunction` that carries its `Signature` and `Summary` beside the Go function. Libraries go through `newLibrary` and types through `RegisterType`. `extensions.Reference()` builds a throwaway runtime with a recorder and keeps what registration adds, so a function and its documentation change in the same entry. Hooks are not registered, because the proxy looks them up by name. Their names are constants shared by the runtime calls and the hook list. Standard Lua functions are read from a state that has only the sandboxed standard libraries open.

A test walks a live extension state, including the `marasi` tables, the globals, and every type's metatable, and fails on a name missing from the reference or a reference name missing from the state. A second test fails on an empty summary or a malformed signature. Signatures use the colon form, because every library function and method reads its first argument at index 2.

**Considered options:** embedding the reference in the skill, which would load it into every session and drift from the code; a separate reference file shipped next to the skill, which `marasi skill > SKILL.md` cannot install; generating the reference from doc comments, which needs a parser and leaves comments unchecked by the compiler.
