# RecoveryLab

**Make a backend fail at the worst moment. Check what survived.**

RecoveryLab is a small, local-first Go CLI for testing three easily missed correctness properties in HTTP services:

1. **Lost response:** The server completes an operation, but the client never receives its response and retries. Did the operation happen twice?
2. **Concurrent duplicates:** Many copies of one operation arrive together. Did a check-then-write race apply it more than once?
3. **Crash after acknowledgement:** The server tells the client an operation succeeded, then the process dies. Is the acknowledged state still present after restart?

It makes the failure visible and checks an integer state invariant, such as an operation count or ledger-entry count. The output is a timeline with the expected and observed values. RecoveryLab uses only Go's standard library; it does not need Docker, a cloud account, or a test framework.

## See it work

With Go 1.23 or newer:

```sh
go run ./cmd/recoverylab demo
```

Or build a binary:

```sh
go build -o recoverylab ./cmd/recoverylab
./recoverylab demo
```

On Windows, build with `go build -o recoverylab.exe ./cmd/recoverylab` and run `./recoverylab.exe demo` in PowerShell.

The self-contained demo runs four experiments against tiny persistent HTTP services:

```text
correct/lost-response                  PASS       expected=PASS  state=1/1
duplicate-bug/lost-response            VIOLATION  expected=VIOLATION  state=2/1
correct/concurrent-duplicates          PASS       expected=PASS  state=1/1
race-bug/concurrent-duplicates         VIOLATION  expected=VIOLATION  state=16/1
correct/crash-after-ack                PASS       expected=PASS  state=1/1
early-ack-bug/crash-after-ack          VIOLATION  expected=VIOLATION  state=0/1

Demo passed: healthy cases held; duplicate, race, and durability defects were detected.
```

The faulty services are intentional: one ignores idempotency keys, one separates the key check from the write, and one acknowledges a write before persisting it. The healthy service uses an append-only journal and flushes each operation before acknowledging it.

## Test your own local service

Write a JSON scenario. This example assumes your service is running on `127.0.0.1:8080` and exposes an integer `count` at `GET /state`:

```json
{
  "name": "create-order-once",
  "scenario": "lost-response",
  "timeout": "20s",
  "request": {
    "method": "POST",
    "url": "http://127.0.0.1:8080/orders",
    "headers": {
      "Content-Type": "application/json",
      "Idempotency-Key": "recoverylab-{{run_id}}"
    },
    "body": { "sku": "book", "quantity": 1 }
  },
  "observe": {
    "url": "http://127.0.0.1:8080/state",
    "pointer": "/order_count",
    "expected_delta": 1
  }
}
```

`{{run_id}}` becomes a new random value on every run. Put it in the request's idempotency key and, if useful, its URL or JSON body. The observation must represent the **side effect**, not just an HTTP status. RecoveryLab reads it before and after the experiment and expects `final = baseline + expected_delta`.

```sh
recoverylab validate scenario.json
recoverylab run scenario.json
recoverylab run --format json --output report.json scenario.json
```

`examples/lost-response.json` works against the included fixture. In one terminal, run `recoverylab fixture --mode correct --listen 127.0.0.1:8080 --journal .recoverylab/fixture.jsonl`; in another, run `recoverylab run examples/lost-response.json`. Replace `correct` with `duplicate-bug` and use a fresh journal path to see a violation.

For an overlap test, use `examples/concurrent-duplicates.json`. It sends 16 copies of the same request with the same key. The demo's `race-bug` service passes the sequential lost-response test but fails the concurrent test.

### Testing an acknowledged write through a crash

For `crash-after-ack`, add a managed service. RecoveryLab starts it, waits for readiness, sends the operation, kills the process after a successful HTTP response, starts it again, and checks persisted state:

```json
{
  "name": "acknowledged-order-survives-restart",
  "scenario": "crash-after-ack",
  "service": {
    "command": ["/absolute/path/to/my-service", "--port", "8080"],
    "working_dir": ".",
    "ready_url": "http://127.0.0.1:8080/health",
    "startup_timeout": "10s"
  },
  "request": {
    "method": "POST",
    "url": "http://127.0.0.1:8080/orders",
    "headers": { "Idempotency-Key": "recoverylab-{{run_id}}" },
    "body": { "sku": "book", "quantity": 1 }
  },
  "observe": {
    "url": "http://127.0.0.1:8080/state",
    "pointer": "/order_count",
    "expected_delta": 1
  }
}
```

The service command should launch the server **in the foreground**, without a shell wrapper, and it must preserve its data outside the process. Relative `working_dir` values are based on the scenario file's directory. Run this against a disposable local service and database. The bundled `demo` handles all process setup for you.

## What the result means

| Exit | Status | Meaning |
| --- | --- | --- |
| `0` | `PASS` | The declared state invariant held in this run. |
| `1` | `VIOLATION` | The fault was injected and the observed state broke the invariant. |
| `2` | `ERROR` | Configuration, startup, request, or observation failed; no correctness verdict. |

For `lost-response`, RecoveryLab first forwards the operation to the service and reads its successful response. It closes the client connection without delivering that response, then retries the *same* operation and checks the state. The retry's HTTP status is recorded, but the state invariant determines the verdict. For `concurrent-duplicates`, it releases 2–64 identical requests together. For `crash-after-ack`, it checks durability after restart; this scenario does not retry.

A `PASS` demonstrates the specified invariant for one executed scenario. It does not prove exactly-once behavior for every workload, nor can a final count reveal all possible hidden side effects. Use a dedicated local test environment with a read endpoint that directly reflects the effect you care about. The observer currently reads an integer JSON value selected by an [RFC 6901 JSON Pointer](https://www.rfc-editor.org/rfc/rfc6901), such as `/count` or `/accounts/alice/operations`.

RecoveryLab accepts loopback URLs only (`localhost`, `127.0.0.1`, `::1`) and does not follow redirects when sending operations. It is designed for local experiments, not production traffic.

## Design

```text
scenario JSON
   │
   ├─ validate local target and invariant
   ├─ optionally start foreground service and wait for readiness
   ├─ read baseline state
   ├─ inject the selected failure
   │    ├─ lost response: forward → consume upstream reply → drop client connection → retry
   │    ├─ concurrent duplicates: release identical operations together
   │    └─ crash after ack: request → receive ack → kill → restart
   ├─ read final state
   └─ report timeline + PASS / VIOLATION / ERROR
```

The project keeps the experiment engine separate from the CLI and the demo fixture. Reports are available as readable terminal output or JSON for CI. The fixture's append-only journal makes restart behavior observable without an external database. See [design notes](docs/design.md) for the exact fault timelines and verdict boundaries.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/recoverylab
```

The tests exercise both fault scenarios against correct and faulty services, strict config validation, JSON Pointer selection, and process restart. CI runs tests, vet, build, and the demo on Windows and Linux.

## Scope

Version 0.1 focuses on one HTTP operation and one integer state observation per experiment. Useful next extensions are a configurable retry response assertion, multiple state invariants, and an explicit failpoint protocol for testing crashes *inside* an operation. Those are outside the claims of the current release.

## License

MIT. See [LICENSE](LICENSE).
