# A retry after restarting a service with SQLite-backed idempotency

The earlier [idempotency checks](idempotency-contract-checks.md) used memory stores. They showed that RecoveryLab could test lost replies and overlapping requests, but a process restart would erase those stores. I wanted to try a backend that keeps its replay record on disk.

On 27 September 2026, I tested [idempot-dev/idempot-js at `eec71828`](https://github.com/idempot-dev/idempot-js/tree/eec71828ef2792395d8eeca2cb9d27c280cf75af), using its Hono middleware and SQLite store. I downloaded that exact commit and compared its 252 repository files with the working checkout. All matched, apart from a temporary change to the package manager build allowlist needed by my older local pnpm. I did not edit the middleware or store source.

The [small host](experiments/idempotency/idempot-js-sqlite-host.mjs) adds an `/orders` handler and a `/state` count endpoint. It puts the business rows and the middleware's idempotency records in the same SQLite file. RecoveryLab starts the host, creates an order, kills the process after HTTP 201, restarts it, checks the row count, retries with the same key and body, and checks the count and `/id` again. Each run gets a fresh key and item name. The item name matters: this middleware also rejects the *same payload under a different key* with HTTP 409, so repeating a fixed body would not be a valid fresh-operation test.

| Mode | Runs | State immediately after restart | State after retry | Retry reply | Result |
| --- | ---: | --- | --- | --- | --- |
| SQLite guard enabled | 20 | One new row in every run | Still one new row | HTTP 201 with the original `/id` | 20 `PASS` |
| Same host, guard disabled | 5 | One new row in every run | Two new rows | HTTP 201 with a different `/id` | 5 `VIOLATION` |

The [per-run CSV](experiments/idempotency/idempot-js-sqlite-runs.csv) includes the fresh run ID, baseline, count before retry, final count, HTTP codes, and verdict. Here are [one guarded report](experiments/idempotency/idempot-js-sqlite-pass.json) and [one control report](experiments/idempotency/idempot-js-sqlite-control.json) with the full event timelines. The other full reports stayed in a temporary working directory; the CSV preserves the measurements without adding 25 near-identical JSON files.

## Reproduce it

Download or clone the pinned idempot-js commit above and install its workspace dependencies using the package manager version in its `package.json`. Its SQLite package uses the native `better-sqlite3` binding. My run used Node 20.17.0, pnpm 10.12.3, and better-sqlite3 12.6.2 on Windows. Because that pnpm version predates the checkout's `allowBuilds` setting, I allowed the native SQLite build in the temporary checkout and ran its standard install script. The root `prepare` hook failed under a production-only install because Husky was absent; the modules loaded and the host ran for the checks above.

Copy [the scenario template](experiments/idempotency/idempot-js-sqlite-crash.template.json). Replace its three `C:/path/to/...` entries with absolute paths to this repository's test host, the idempot-js checkout, and a disposable SQLite file. Keep the host and scenario on the same port. With a built `recoverylab` binary:

```sh
recoverylab validate idempot-js-sqlite-crash.json
recoverylab run --repeat 20 idempot-js-sqlite-crash.json
```

For the control, change the last `service.command` argument from `guard` to `control`, use a new SQLite file, and run five times. Every control run should report `VIOLATION`. This changes only the test host's use of the upstream middleware; it does not change upstream code.

This is a local contract check, **not a bug found in idempot-js**. It covers a process kill after a successful reply, one Node process at a time, and one SQLite file. It does not test a crash during the handler or between the business insert and the middleware's replay write, a database or machine failure, multiple service instances, or a payment provider. The count and selected `/id` are the things verified; other side effects could still be wrong.
