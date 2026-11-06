# Changelog

## 0.6.0

Stricter checks, a guide to fixing what they find, and faster installs.

- A retry must now return the same IDs as the lost reply. `audit` compares fields such as `id` and `order_id` automatically, and `retry_response.compare_ids` turns it on for `run`. Per-request fields like `request_id` are ignored.
- A concurrent burst fails on any 5xx in an audit, even when the count is right. A duplicate in flight should get 409. `concurrent_response.no_server_errors` turns this on for `run`.
- [The fixing guide](https://github.com/Wali05/recoverylab/blob/main/docs/fixing.md) explains the idempotency-key pattern with PostgreSQL and Go code, and every finding links to the section that fixes it.
- `{{env:NAME}}` in header values reads secrets from the environment, so tokens stay out of scenario files.
- `install.sh` installs the latest release on macOS and Linux after checking its SHA-256 sum.
- The GitHub Action downloads and verifies the release binary when pinned to a release instead of building from source.
- Release notes start with the version's changelog section.
- CI runs `staticcheck` and installs the published release with `install.sh`. Added issue forms, Dependabot, and security and contributing notes.

## 0.5.0

The big change: you no longer have to write scenarios by hand or read raw numbers to understand a failure.

- `audit` runs every failure that applies to a request (lost response, concurrent bursts, key reuse, and crash after ack) and prints a report card. It takes one file, several, or a folder.
- `discover` reads an OpenAPI 3 or Swagger 2 JSON document from your running service or a file and writes a scenario for every `POST` it can count, with bodies built from the schema.
- `init` looks at a state endpoint, finds the counters and lists in it, and writes a scenario for one endpoint.
- Every `VIOLATION` and `ERROR` explains what happened, the likely cause, and what usually fixes it.
- `"measure": "length"` counts the items in a list, so a plain `GET /orders` works as a state check.
- New report formats: `--html` (single file, light and dark), `--junit` for CI test tabs, `--markdown` for job summaries and PR comments, and `--badge` for a README badge.
- The repository is a GitHub Action.
- A JSON Schema for scenario files gives editors autocomplete and inline errors.
- Terminal output uses color when it's a terminal, and respects `NO_COLOR`.
- The fixture now serves `GET /operations` and `/openapi.json`.

Scenario files from 0.4 still load and run the same way.

## 0.4.2

- `key-reuse` checks that a changed body with a reused key is rejected and writes nothing.

## 0.4.1

- Stricter scenario parsing. Tested crash retries against a SQLite-backed idempotency middleware.

## 0.4.0

- `crash-after-ack` can retry the same request after restart and checks state before and after that retry.

## 0.3.0

- `concurrent_response.allowed_statuses` checks the HTTP codes returned by a burst.

## 0.2.0

- `retry_response` checks the retry's status and compares selected JSON values with the original reply.
- Tried against PocketBase, a Spring sample, and 47 more small APIs.

## 0.1.2

- Fixed the version shown after `go install`.

## 0.1.1

- Simpler demo output.

## 0.1.0

- First release: `lost-response`, `concurrent-duplicates`, and `crash-after-ack`, repeat runs, and multiple state checks.
