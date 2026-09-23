# Design notes

RecoveryLab is a **black-box experiment runner**. It has no access to the target service's internal database or business rules. The scenario author supplies an observable integer that represents the effect being tested. That makes the tool applicable to many HTTP services, while keeping its verdict precise: it checks one declared invariant in one run.

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

The retry reuses the **same method, URL, body, and headers**, including the idempotency key. The request can include `{{run_id}}`, resolved once before the experiment, so runs do not collide with one another.

## Concurrent duplicates

The runner creates N goroutines, each holding the same request until a shared start channel closes. It records each HTTP status. The overlap is a best-effort local experiment; an external scheduler cannot guarantee that every handler reaches a particular code line simultaneously. The fixture deliberately pauses between its key check and write so the demo reproduces that race reliably.

## Crash after acknowledgement

```text
start service → ready probe → baseline → POST → 2xx response
                                               ↓
                                            kill process
                                               ↓
                                  restart → ready probe → final state
```

RecoveryLab manages a foreground process and forcibly kills it after receiving the operation's successful response. It then starts the same command again. This probes whether an *acknowledged* effect survived a process crash. It does not simulate a machine power loss, disk failure, or a crash at every possible instruction. Testing a precise in-operation crash point would require an explicit failpoint protocol in the target service.

The readiness URL must not already return 2xx before the managed process starts. That check prevents a leftover server on the same port from creating a false pass. The command should launch the actual server executable without a shell wrapper, so the process being killed is the process serving requests.

## Verdicts and their limits

The invariant is `final integer = baseline integer + expected_delta`. `PASS` means this equality held after the injected scenario. `VIOLATION` means the experiment completed and the equality failed. `ERROR` means the runner could not establish the experiment's preconditions or read the state reliably.

One final count cannot distinguish every incorrect history. A service could create and later delete duplicate records, or mutate an unobserved field. Choose an observation that directly reflects the effect of interest, such as a ledger-entry count for a payment operation. Keep the target isolated from other writers while the experiment runs; otherwise unrelated writes can change the observed value.

The retry response is reported but does not currently determine the verdict. This is deliberate: services can legally choose different duplicate-response policies, while the side-effect invariant is specified by the scenario author. A future version could make accepted retry statuses part of the scenario contract.

## Failure handling

- The entire experiment has a context deadline. A hung request or readiness probe ends as `ERROR`.
- Managed processes are stopped on completion and on setup failures. The CLI keeps the last 32 KiB of process output for startup diagnostics.
- Operation URLs are loopback-only, and redirects are not followed. This keeps experiments on a local test environment.
- Terminal and JSON reports use the same structured result. Exit codes are stable: 0 pass, 1 violation, 2 error.
