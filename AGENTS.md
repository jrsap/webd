# webd

A tiny Go webserver: static files, proxy routes, and on-demand container routes via podman.
See README.md for the config format and design decisions.

## Development principles

### TDD is mandatory
1. Design the interface/API first.
2. Write failing tests against it.
3. Implement only what is needed to make them pass.

No new behavior without a test first. Bug fixes get a failing reproduction test before the fix.

### Less is more
- Prefer the Go standard library; every external dependency must justify its existence.
- One static binary, no daemons beyond podman itself.
- No speculative abstractions, options, or config keys - add them when a concrete need exists.
- The smallest config surface that solves the problem.

### Auditability
- Explicit code over cleverness; small functions, small API surface.
- Fail fast and loud at startup on invalid config.
- Container operations shell out to the `podman` CLI rather than reimplementing
  image pulls, storage, or networking in-process.
