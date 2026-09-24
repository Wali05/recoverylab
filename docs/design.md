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

The retry reuses the **same method, URL, body, and headers**. If you include an idempotency key, it reuses that too. You can put `{{run_id}}` in the request; RecoveryLab fills it in once before the experiment so separate runs do not share a key. The retry must return 2xx by default. A scenario can list other accepted HTTP codes with `retry_response.allowed_statuses` and can compare selected JSON values with `retry_response.same_json_pointers`. The proxy holds the original reply in memory for that comparison; it still sends none of it to the client.

## Concurrent duplicates

The runner starts one goroutine per request, then releases them together. It records each HTTP status. This makes the requests overlap, but it cannot force the target handlers to reach the same line of code at the same moment. The built-in test server pauses between checking a key and writing state so the demo can reproduce the race.

`run --repeat N` executes separate experiments with fresh run IDs. It helps reveal intermittent failures but does not coordinate arrival inside the target handler or guarantee discovery of a narrow race.

## Crash after acknowledgement

```text
start service → ready probe → baseline → POST → 2xx response
                                               ↓
                                            kill process
                                               ↓
                                  restart → ready probe → state check
                                                       ↓ optional
                                          retry same request → final state
```

RecoveryLab manages a foreground process and forcibly kills it after receiving the operation's successful response. It then starts the same command again. This probes whether an *acknowledged* effect survived that application process crash. When `retry_response` is present, it reads each declared state value **before** resending the same request. It compares the retry reply and reads state again afterward. The first check matters: otherwise a retry that recreates a lost write could make the final count look correct. Without `retry_response`, the original durability-only check still reads state once after restart. RecoveryLab does not kill an external database, simulate a machine power loss or disk failure, or crash at every possible instruction. Testing a precise in-operation crash point would require an explicit failpoint protocol in the target service.

The readiness URL must not already return 2xx before the managed process starts. That check prevents a leftover server on the same port from creating a false pass. The command should launch the actual server executable without a shell wrapper, so the process being killed is the process serving requests.

## Verdicts and their limits

Each state invariant is `observed integer = baseline integer + expected_delta`. `PASS` means every declared check held, including response checks and, for a crash with retry, both the before-retry and final state checks. `VIOLATION` means at least one check definitely failed. `ERROR` means the runner could not establish the experiment's preconditions or read enough state for a verdict. If the original reply lacks a JSON value a scenario asks to compare, the comparison is inconclusive; if the retry lacks or changes that value, the result is a `VIOLATION`. A durability mismatch already observed after restart remains a `VIOLATION` even if a later retry cannot be completed; the report also says that retry was incomplete.

Even several final counts cannot distinguish every incorrect history. A service could create and later delete duplicate records, or mutate an unobserved field. Choose checks that directly reflect the effects of interest, such as both payment and ledger-entry counts. Keep the target isolated from other writers while the experiment runs; otherwise unrelated writes can change the observed values.

RecoveryLab records HTTP codes from retries and concurrent requests. A lost-response retry, and a post-restart retry when enabled, must return 2xx unless the scenario declares exact allowed codes. Concurrent response codes are informational unless `concurrent_response.allowed_statuses` is set. Selecting JSON pointers for retry comparison is optional and should target stable fields such as a resource ID. RecoveryLab does not compare whole responses, headers, or latency.

## Failure handling

- The entire experiment has a context deadline. A hung request or readiness probe ends as `ERROR`.
- Managed processes are stopped on completion and on setup failures. The CLI keeps the last 32 KiB of process output for startup diagnostics.
- Operation URLs are loopback-only, and redirects are not followed. This keeps experiments on a local test environment.
- The observer rejects responses larger than 1 MiB, incomplete JSON, and extra JSON values. Response comparison also caps each reply at 1 MiB and rejects ambiguous JSON. A response body must finish before an HTTP acknowledgement counts as complete.
- A managed-service scenario executes its `service.command` locally; only run scenario files you trust.
- Terminal and JSON reports use the same structured result. Exit codes are stable: 0 pass, 1 violation, 2 error.
