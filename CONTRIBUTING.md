# Contributing

Bug reports with a scenario file attached are the most useful thing you can send. If a verdict looks wrong, include what your service's state endpoint returned before and after.

## Working on the code

RecoveryLab uses only Go's standard library, and I'd like to keep it that way. You need Go 1.23 or newer.

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/recoverylab demo
```

CI also runs `staticcheck`, the action against the built-in fixture, and `discover` followed by `audit`.

- `internal/engine` injects the faults and decides each verdict.
- `internal/audit` turns one scenario into the full set of checks.
- `internal/explain` writes the plain-language explanations; each one should link to a section of `docs/fixing.md`.
- `internal/openapi` reads OpenAPI documents for `discover`.
- `internal/report` writes HTML, JUnit, Markdown, and badges.
- `internal/fixture` is the small service with deliberate bugs that the demo and tests run against.

A new check should come with a fixture mode, or a test server, that fails it, so the test proves the check can actually catch the bug. A new scenario setting needs an entry in `docs/scenario.schema.json`; a test fails if it's missing.

## Releasing

Add a section for the version to `CHANGELOG.md`, then start **Release draft** from the Actions tab with the version, such as `v0.6.0`. The workflow tests, builds every binary with checksums, and opens a draft release whose notes start with that changelog section. Publishing the draft creates the tag.
