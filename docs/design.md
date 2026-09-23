# Design notes

RecoveryLab tests a service from the outside. It cannot see the service's database or business rules, so you tell it which state values to check. A result applies only to those values and that run.

## Lost response

```text
RecoveryLab client       local fault proxy           service
       │                        │                       │
       ├── POST (key K) ────────►│                       │
       │                        ├── POST (key K) ───────►│
       │                        │◄── 2xx response ──────┤
       │◄── connection closed ──┤                       │
       ├── POST (key K) ─────────────────────────────────►│
       │◄── retry response ──────────────────────────────┤
       ├── GET state ────────────────────────────────────►│
       │◄── final value ──────────────────────────────────┤
```

The proxy accepts exactly one client operation. It reads the complete client request body, sends the same operation upstream, consumes the upstream response, and then hijacks and closes the client connection before sending any HTTP response. The runner only calls this a successfully injected fault when the upstream acknowledged with a 2xx status and the client observed a connection error. A failed upstream request or a failed proxy injection is an `ERROR`, not a correctness verdict.

The retry reuses the **same method, URL, body, and headers**. If you include an idempotency key, it reuses that too. You can put `{{run_id}}` in the request; RecoveryLab fills it in once before the experiment so separate runs do not share a key.

## Concurrent duplicates

The runner starts one goroutine per request, then releases them together. It records each HTTP status. This makes the requests overlap, but it cannot force the target handlers to reach the same line of code at the same moment. The built-in test server pauses between checking a key and writing state so the demo can reproduce the race.

`run --repeat N` executes separate experiments with fresh run IDs. It helps reveal intermittent failures but does not coordinate arrival inside the target handler or guarantee discovery of a narrow race.

## Crash after acknowledgement

```text
start service → ready probe → baseline → POST → 2xx response
                                               ↓
                                            kill process
                                               ↓
                                  restart → ready probe → final state
```

RecoveryLab manages a foreground process and forcibly kills it after receiving the operation's successful response. It then starts the same command again. This probes whether an *acknowledged* effect survived that application process crash. It does not kill an external database, simulate a machine power loss or disk failure, or crash at every possible instruction. Testing a precise in-operation crash point would require an explicit failpoint protocol in the target service.

The readiness URL must not already return 2xx before the managed process starts. That check prevents a leftover server on the same port from creating a false pass. The command should launch the actual server executable without a shell wrapper, so the process being killed is the process serving requests.

## Verdicts and their limits

Each invariant is `final integer = baseline integer + expected_delta`. `PASS` means every declared check held after the injected scenario. `VIOLATION` means the experiment completed and at least one check failed. `ERROR` means the runner could not establish the experiment's preconditions or read the state reliably.

Even several final counts cannot distinguish every incorrect history. A service could create and later delete duplicate records, or mutate an unobserved field. Choose checks that directly reflect the effects of interest, such as both payment and ledger-entry counts. Keep the target isolated from other writers while the experiment runs; otherwise unrelated writes can change the observed values.

RecoveryLab reports the HTTP codes from retries and concurrent requests, but your state check decides `PASS` or `VIOLATION`. Services handle duplicate responses differently, so a `PASS` does not tell you whether the response codes matched your API contract.

## Failure handling

- The entire experiment has a context deadline. A hung request or readiness probe ends as `ERROR`.
- Managed processes are stopped on completion and on setup failures. The CLI keeps the last 32 KiB of process output for startup diagnostics.
- Operation URLs are loopback-only, and redirects are not followed. This keeps experiments on a local test environment.
- The observer rejects responses larger than 1 MiB, incomplete JSON, and extra JSON values. A response body must finish before an HTTP acknowledgement counts as complete.
- A managed-service scenario executes its `service.command` locally; only run scenario files you trust.
- Terminal and JSON reports use the same structured result. Exit codes are stable: 0 pass, 1 violation, 2 error.
