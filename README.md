# RecoveryLab

**A local failure lab for APIs that write data.**

Imagine your API creates an order and sends `200 OK`, but the response disappears on the way back. The client retries. RecoveryLab recreates that moment, then checks whether the order was created once or twice.

It also tests a burst of duplicate requests and a process crash after a successful response. RecoveryLab works with any local HTTP service that exposes the state you want to check. It judges the observed side effect, not just the HTTP status.

![Terminal preview of the six built-in RecoveryLab demo cases](docs/assets/demo.png)

## See it run

With [Go 1.23+](https://go.dev/dl/) installed, run this from the repository root:

```sh
go run ./cmd/recoverylab demo
```

The demo starts its own services. It needs no Docker, account, or API key. It compares three healthy fixtures with three deliberately broken ones:

```text
A write succeeds, its reply vanishes, and the client retries. (observed/expected)
Lost response: correct PASS 1/1 | duplicate bug VIOLATION 2/1
16 requests at once: correct PASS 1/1 | race bug VIOLATION 16/1
Crash after 2xx: correct PASS 1/1 | early ack bug VIOLATION 0/1
Demo passed: 3 healthy cases held; 3 injected defects were detected.
```

The numbers are observed/expected state values. `VIOLATION 2/1` means two writes occurred when the scenario expected one. A `VIOLATION` is the *correct* result for a deliberately buggy fixture.

The fixture makes its race and early-ack bugs repeatable. That demonstrates how the checks work; it does not predict how often a run will expose a bug in your service.

In a separate check, RecoveryLab ran against [NetCore](https://github.com/Wali05/NetCore), a Spring Boot service using an in-memory H2 database. After its `allocate-next` endpoint returned 200 but the reply was dropped, a retry raised the allocated-address count from 0 to 2. RecoveryLab reported `VIOLATION` for a scenario expecting one allocation. [The check and its limits](docs/independent-check.md) explain exactly what this shows.

## Install

If you have Go 1.23 or newer:

```sh
go install github.com/Wali05/recoverylab/cmd/recoverylab@latest
recoverylab demo
```

You can also [download a release binary](https://github.com/Wali05/recoverylab/releases) for Windows, macOS, or Linux once a release is published. Put it on your `PATH` (`recoverylab.exe` on Windows). Releases include SHA-256 checksums; binaries are currently unsigned.

To build from a checkout, run `go build -o recoverylab ./cmd/recoverylab` (`go build -o recoverylab.exe ./cmd/recoverylab` on Windows).

## What it tests

| Scenario | What RecoveryLab does | What you learn |
| --- | --- | --- |
| `lost-response` | Lets the service process a request, drops the reply, then sends the same request again. | Whether a retry repeats the side effect. |
| `concurrent-duplicates` | Sends 2–64 copies of the same request together. | Whether this burst caused a duplicate side effect. |
| `crash-after-ack` | Starts your service, sends a request, kills its process after a 2xx response, then restarts it. | Whether the exposed state survived that process crash. |

RecoveryLab reads your state endpoint before and after the experiment. For each check, it compares the final integer with `baseline + expected_delta`. It can only judge the state you expose; a passing run is not proof that every possible side effect is safe.

## Try it against your service

Use a disposable local service with a write endpoint and a `GET` endpoint that exposes the effect as an integer. Keep other writers out of the test while it runs. Here is a scenario for a service listening on `127.0.0.1:8080`:

```json
{
  "name": "order-is-created-once",
  "scenario": "lost-response",
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

Save that as `scenario.json`, change the URLs and JSON pointer to match your service, then run:

```sh
go run ./cmd/recoverylab validate scenario.json
go run ./cmd/recoverylab run scenario.json
```

`{{run_id}}` is replaced once per run, so the original request and retry share a key, while a later run gets a fresh one. Put it in the idempotency key, URL, or JSON body wherever your service identifies an operation. The observation must reflect the side effect you care about; for example, an order count can reveal duplicate orders, while an HTTP 200 alone cannot.

If the state endpoint needs authentication, add `"headers": { "Authorization": "Bearer ..." }` inside `observe`. Keep credentials out of files you commit.

One count can hide another broken effect. To check several effects, replace `observe` with named `checks` (up to 16). For example, checking orders alone would miss a duplicate charge:

```json
{
  "checks": [
    { "name": "orders", "url": "http://127.0.0.1:8080/state", "pointer": "/order_count", "expected_delta": 1 },
    { "name": "charges", "url": "http://127.0.0.1:8080/state", "pointer": "/charge_count", "expected_delta": 1 }
  ]
}
```

Keep the `name`, `scenario`, and `request` fields from the first example; the block above only shows the replacement for `observe`.

To see the same flow without writing a service, start the included fixture in one terminal:

```sh
go run ./cmd/recoverylab fixture --mode correct --listen 127.0.0.1:8080 --journal .recoverylab/fixture.jsonl
```

Then, in a second terminal:

```sh
go run ./cmd/recoverylab run examples/lost-response.json
```

Stop the fixture before changing its mode. To try the duplicate bug, start it with `--mode duplicate-bug` and a **new journal path**, then run the same scenario. For overlapping requests, use [examples/concurrent-duplicates.json](examples/concurrent-duplicates.json) and the fixture's `race-bug` mode.

For a Docker Compose service, [publish its HTTP port on host loopback](https://docs.docker.com/get-started/docker-concepts/running-containers/publishing-ports/), for example `ports: ["127.0.0.1:8080:8080"]`, and use `http://127.0.0.1:8080` in the scenario. RecoveryLab cannot directly address a container-only bridge IP from the host. The managed-process crash scenario requires a foreground process it can kill; it does not kill a Docker container.

### Test a crash after a successful response

This scenario needs a foreground service that RecoveryLab can start and kill. In the scenario above, change `scenario` to `crash-after-ack` and add a `service` key with this object as its value:

```json
{
  "command": ["/absolute/path/to/my-service", "--port", "8080"],
  "ready_url": "http://127.0.0.1:8080/health",
  "startup_timeout": "10s"
}
```

The command must run the server process directly and store its data outside the process. Relative command paths use `service.working_dir`, which defaults to the scenario file's directory. Only run scenario files you trust: this command executes on your computer.

RecoveryLab waits for `ready_url`, reads the baseline, sends the operation, kills the process after a 2xx response, restarts it, and reads state again. Use an endpoint whose 2xx response means the write should already be durable. This tests a **process crash after acknowledgement**; it does not crash a separate database, simulate a power loss, or choose a crash point inside your handler. See [design notes](docs/design.md) for the precise timeline.

## Read the result

| Exit code | Result | Meaning |
| --- | --- | --- |
| `0` | `PASS` | All declared state checks matched in this run. |
| `1` | `VIOLATION` | The fault ran, and at least one state check failed. |
| `2` | `ERROR` | Setup, a request, or an observation failed; there is no correctness verdict. |

These are the built `recoverylab` binary's exit codes. `go run` can wrap a nonzero exit; use the built binary when checking exit codes in CI.

For a machine-readable report, run `go run ./cmd/recoverylab run --format json --output report.json scenario.json`. The JSON includes the request outcomes and a timestamped event timeline. The integer is selected with an [RFC 6901 JSON Pointer](https://www.rfc-editor.org/rfc/rfc6901), such as `/count`.

RecoveryLab accepts loopback HTTP(S) URLs only and does not follow redirects. Its current scope is one operation and up to 16 named integer checks per run. Those checks can still miss effects that your state endpoints do not show.

The retry and concurrent response codes are recorded but do not decide `PASS` or `VIOLATION`; the declared state check does. The request body and observation response are limited to 1 MiB each.

A single run may miss an intermittent race. With the fixture or your service running, use `go run ./cmd/recoverylab run --repeat 20 examples/concurrent-duplicates.json` to try 20 independent bursts; each gets a fresh `{{run_id}}`. A batch reports every result and exits with a violation if any run found one. With `--format json` or `--output`, repeated runs produce one JSON object containing a `reports` array. Asynchronous or eventually consistent writes need a different check because RecoveryLab reads state immediately after the requests finish.

## Develop

```sh
go test ./...
go vet ./...
```

The project uses only Go's standard library. Tagged builds produce Windows, Linux, and macOS binaries for GitHub Releases; see the [release workflow](.github/workflows/release.yml).

MIT licensed. See [LICENSE](LICENSE).
