# 0004. Adopt declscope as a required lint

- Status: Accepted
- Date: 2026-10-04

## Context

Within a Go package, every package-level declaration is visible from every file. Splitting code into files does not express any boundary.
Once a helper function in one file starts being used from another file, the package gradually turns into a single lump.
gotwamp has a lot of code with distinct responsibilities, such as the protocol layer, state machines, and the socket layer, and several contributors implement it in parallel.
We want to keep per-file responsibility boundaries mechanically. In addition, an exported identifier under `internal/` that is not used from outside
claims a dependency that does not actually exist.

## Decision

- Make [declscope](https://github.com/mpyw/declscope) a required lint, run by `make lint` and in CI
- Run `declscope shrink ./...` first, then `declscope ./...`. Unexporting unnecessary exports with `shrink` reveals new boundary violations, so clear those first
- The principle is one file = one namespace. Declarations are file-private by default. Only declarations used from other files are marked explicitly with `//declscope:package // <reason>`
- Fix diagnostics by changing structure, such as splitting or merging files, not by scattering directives. Do not hide boundaries with `//declscope:core` or a file-wide `//declscope:package`
- Use the default settings (boundary on, surplus strict, unused strict, naming rule off). `.declscope.yaml` only excludes generated code (`internal/api/gen`) via `filter.omit`
- Do not create a baseline (this is a new repository, so keep zero diagnostics from the start)

## Options considered

| Option | Pros | Cons |
|--------|------|------|
| Require declscope with default settings (chosen) | Dependencies between files are explicit, and changes to boundaries are visible in review. `shrink` prevents excessive exports | Writing directives and planning the file layout takes effort |
| Also enable the naming rule (`qualify`) | The owning file can be inferred from the name | It enforces a convention rather than a boundary, and fires in large numbers where there is no boundary problem. Decide separately if it becomes necessary |
| Do not adopt | No effort | Boundary erosion cannot be detected |

## Consequences

- In each package, dependencies between files are made explicit as `//declscope:package`, with the reason recorded
- When adding a new file, you must decide which declarations to expose to other files
- Do not set `rules.boundary: off` to silence declscope diagnostics
