# What happens if a client reuses a key for a different order?

An idempotency key is meant to identify one operation. An accidental second request with the same key but a different body has two bad possible outcomes: it creates another record, or the server silently returns the first operation's result as if it applied the new request. A count catches the first mistake but misses the second.

The `key-reuse` scenario checks both. RecoveryLab sends an original request, then sends a **changed JSON body with the same method, URL, headers, and key**. It expects an exact 4xx status, 422 by default, and checks that the declared state changed only once. The [IETF's expired Internet-Draft](https://datatracker.ietf.org/doc/html/draft-ietf-httpapi-idempotency-key-header#section-2.7) recommends 422 for this case. It is a draft, not a finalized standard. [Stripe's documented behavior](https://docs.stripe.com/api/idempotent_requests) also rejects a reused key when the parameters differ, without promising that every API uses 422. Set `expected_reuse_status` to your API's documented 4xx code if it differs.

```text
POST /orders  key=K  {"item":"book"}  → 201 Created
POST /orders  key=K  {"item":"pen"}   → 422 expected
GET /state                         → one new order expected
```

The example [scenario file](../examples/key-reuse.json) works with the built-in fixture. Start `fixture --mode correct` in one terminal and run the example in another. Start `fixture --mode duplicate-bug` with a fresh journal to see a `VIOLATION`. Keep `{{run_id}}` in the key when repeating a scenario: each run needs a key the service has not seen before.

## Check against an outside project

On 27 September 2026, I ran this scenario against the [pinned idempot-js checkout](https://github.com/idempot-dev/idempot-js/tree/eec71828ef2792395d8eeca2cb9d27c280cf75af) already used in the [SQLite crash/retry check](persistent-idempotency-check.md). The [small local host](experiments/idempotency/idempot-js-sqlite-host.mjs) uses the project's Hono middleware and SQLite store. Its handler writes an order with the requested item. RecoveryLab first asks for a book, then asks for a pen with the same key. Each run uses a new key and item name.

| Setup | Runs | Reply to changed request | Order count per run | Verdict |
| --- | ---: | --- | --- | --- |
| Upstream middleware enabled | 20 | HTTP 422 each time | One new order | 20 `PASS` |
| Same host, middleware disabled | 5 | HTTP 201 each time | Two new orders | 5 `VIOLATION` |

The [per-run CSV](experiments/idempotency/idempot-js-key-reuse-runs.csv) records the baseline, expected and observed counts, both HTTP codes, and verdict. [One guarded report](experiments/idempotency/idempot-js-key-reuse-guard.json) and [one control report](experiments/idempotency/idempot-js-key-reuse-control.json) show the full event timelines. The control changes only whether the host installs the middleware; it does not edit upstream source.

To reproduce the run, install the pinned checkout as described in the [earlier SQLite check](persistent-idempotency-check.md), then copy the [scenario template](experiments/idempotency/idempot-js-sqlite-key-reuse.template.json). Replace its absolute paths with your local paths and run `recoverylab run --repeat 20 your-scenario.json`. For the control, use a **new SQLite file**, change the last `service.command` argument to `control`, and run five times. RecoveryLab starts and stops the host for each run.

This is a contract check that **passed** for idempot-js in this setup, not a discovered upstream bug. The disabled guard proves that RecoveryLab detects this host's unsafe behavior. A `PASS` only covers the response code and the order count exposed by this host. It does not prove that every field, external side effect, key-expiry policy, or multi-instance deployment is correct.
