# gotwamp development guidelines

gotwamp is a TWAMP (RFC 5357) implementation written in Go: Session-Sender and Session-Reflector, TWAMP Full
and Light, built as two binaries, the daemon `gotwampd` and the CLI `gotwamp`. Module path
`github.com/Akkanbe/gotwamp`, Apache-2.0.

## Required reading

- `docs/SPEC.md`: the implementation specification. **Read the chapters of your area before you start.** Do not
  implement anything that contradicts it. The defaults are in Appendix A.
- `docs/PLAN.md`: the implementation plan. §5 repository layout, §6 milestones, §9 quality criteria.
- `docs/research/`: research on the RFCs, other implementations and vendors. Consult it when in doubt.

## Language

- Everything is in **English**: code, identifiers, comments, commit messages, test names, and every document
  (`docs/`, README, ADRs, CHANGELOG).

## Dependency direction (SPEC §4.3)

```
cmd → internal/{daemon,cli} → internal/{schedule,store,export,config} → internal/{control,sender,reflector}
    → internal/{tstamp,clock,stats} → internal/twamp/{wire,ntptime,errest}
```

- `internal/twamp/*` and `internal/stats` use the **standard library only**. They do not depend on `net`,
  `time.Now` or randomness (randomness is injected as an `io.Reader`).
- The usual libraries (cobra, grpc, protobuf, prometheus/client_golang, x/sys, yaml.v3) are used in the outer
  layers only.

## Quality criteria

- Go 1.26. `CGO_ENABLED=0`. Linux-only code carries `//go:build linux`.
- Formatting: `gofumpt`. Linting: `golangci-lint` + `go vet` + **`declscope`**.
- `declscope` is a required lint. Run `declscope shrink ./...` and then `declscope ./...`, and keep both clean.
  Follow the skill `declscope-adoption` (`~/.claude/skills/declscope-adoption/SKILL.md`) for its use and for
  fixing its diagnostics.
  The principle: one file = one namespace. Declarations are private to their file by default. Mark only the
  declarations other files use with `//declscope:package // <reason>`. Fix diagnostics with structure
  (splitting or merging files), not with a flood of directives.
- Tests: `go test -race ./...` is required. Coverage of `internal/twamp` and `internal/stats` is 90% or more,
  70% or more overall. Time-dependent logic uses `testing/synctest`. Decoders have fuzz tests. Golden byte
  sequences taken from the RFC figures go in `test/golden/`.
- Exported identifiers have doc comments. Errors are wrapped with `fmt.Errorf("...: %w", err)`.
- Commits: Conventional Commits, in English. **Commit and push only when the user explicitly asks.**

## Working with multiple agents

- Do not change files outside the directories assigned to you. Only the owner (the infrastructure owner) changes
  `go.mod`/`go.sum`. An area that uses the standard library only has no reason to touch `go.mod`.
- Keep your area compilable and testable even when other areas are unfinished (`go build ./... && go test -race
  ./...` passes).
- When done, report briefly: the files created, the test results, the declscope results and the open issues.
