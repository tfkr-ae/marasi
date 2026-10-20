# Coding standards

Conventions this repo follows, most of them enforced in past code reviews. Apply them to new and changed code. Older code that predates a rule is not a finding unless the change touches it. Decisions about behaviour live in `docs/adr/`, and domain terms in `GLOSSARY.md`.

## Design

- **One place for shared logic.** When two places need the same logic, extract one helper and call it from both (`decodeStrictJSON`, `controlRefusal`).
- **Methods a caller depends on belong in the interface.** Add the method to the interface (such as `RepositoryProvider`) and guard it with a `var _ I = (*T)(nil)` check. A comma-ok type assertion that discovers optional behaviour fails silently when a wrapper sits in between. Keep one only with a `var _` guard that makes the method's absence a compile error.
- **Typed kinds, one constant per value.** Model a closed set of string values as a named type (`domain.JobName`) with exactly one constant per value and no aliases.
- **Named constants.** Give a magic number a named, commented constant (`cdpVersionLimit`).
- **Deterministic output.** Output and errors never depend on map iteration order. Check and report in a fixed order.
- **Glossary terms in code.** Identifiers and docs use the terms `GLOSSARY.md` defines, not the ones it lists under _Avoid_ (`trafficMatch`, not `trafficFilter`).

## Concurrency and storage

- **Release before waiting.** Never hold a lock or the project gate while waiting on the network or another process. Release it first (`docs/adr/2026-10-17-project-switch-never-waits-on-the-network.md`).
- **One transaction for writes that land together.** Use `repo.inTx` or `repo.inTxContext`, and check the write's preconditions inside the same transaction.
- **Closures own their errors.** A closure passed to `inTx` or a read callback declares its own `err` rather than assigning to the enclosing function's.
- **Context first.** `ctx context.Context` is the first parameter. A read a client can abandon has a `…Context` variant, and the context-free form calls it.

## Errors

- **Wrap with a lowercase gerund phrase.** `fmt.Errorf("reading configuration: %w", err)`: `": %w"` with no space before the colon.
- **Sentinel errors.** Declare expected failures as `Err… = errors.New(…)` and compare them with `errors.Is`.
- **Refusals carry a code.** A refused control API command returns a machine-readable code with a human message (404 `not_found`, not a bare status).

## Proxy traffic

- **Add no headers.** Every transport that sends captured, proxied, launched or Armory traffic sets `DisableCompression`. Marasi's own code adds no header to a request (`docs/adr/2026-10-19-the-proxy-adds-no-headers-to-a-request.md`, `docs/agents/maps/request-path.md`).

## Comments

- **Say why.** A comment explains the reason or the constraint, not what the next line does. When an ADR decided it, cite the ADR by path.

## Tests

- **Subtests start with "should".** `t.Run("should fail with upstream_error when …", …)`.
- **Failure messages use the wanted/got form.** `t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)`. The standard library is enough; this repo uses no assertion library.
- **Pin claimed behaviour.** A behaviour that docs or skills state, such as a status code or a JSON value, has a test that would fail if it changed.
