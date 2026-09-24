# Checks against projects that promise idempotency

The earlier [50-repo sweep](compatibility-checks-2.md) showed that RecoveryLab could reach many API stacks. Most of those repos were basic CRUD examples with no promise to handle retries. This check asks a narrower and more useful question: when a project **does** promise idempotency, can RecoveryLab verify one write and a usable replay after the first reply is lost?

I ran [RecoveryLab v0.2.0](https://github.com/Wali05/recoverylab/releases/tag/v0.2.0) on 24 September 2026. Each run gave the write a fresh `Idempotency-Key`, let the service acknowledge it, hid that reply, retried the same request, and checked both the side-effect count and the returned operation ID. The 10-run batches below use ten distinct keys each.

| Upstream project at tested commit | What was tested | Guarded: 10 lost-response runs | Control with guard off |
| --- | --- | --- | --- |
| [Ville de Montréal / express-idempotency (`263419a`)](https://github.com/VilledeMontreal/express-idempotency/tree/263419afec78344d6b923a954a9ba62f2282d7f4) | Its published Express middleware in a small local host | [10 `PASS`](experiments/idempotency/express-repeat.json): one handler execution and HTTP 201 with the same `/operation_id` on every retry | [10 `VIOLATION`](experiments/idempotency/express-control-repeat.json): two executions and a changed ID |
| [pypy-riley / idemptx (`b946579`)](https://github.com/pypy-riley/idemptx/tree/b946579163581689a6d3bdcd732c9e153bb5255b) | Its FastAPI decorator with the project's in-memory backend in a small local host | [10 `PASS`](experiments/idempotency/fastapi-repeat.json): one handler execution and HTTP 201 with the same `/operation_id` on every retry | [10 `VIOLATION`](experiments/idempotency/fastapi-control-repeat.json): two executions and a changed ID |
| [Spring Boot idempotency starter (`9346b65`)](https://github.com/arthurfaby/spring-boot-starter-idempotency/tree/9346b655710ebda5c2bae762c2735a1fd3b18ec2) | Its own unmodified payment sample app | [10 `PASS`](experiments/idempotency/spring-repeat.json): one ledger entry per key and HTTP 201 with the same `/paymentId` on every retry | Not run; removing the annotation would modify upstream code |

The Express and FastAPI hosts are [here](experiments/idempotency/express-server.cjs) and [here](experiments/idempotency/fastapi_server.py). They contain a counter and an endpoint that returns an ID. They use the upstream middleware and decorator **without modifying upstream source**. Switching each host to `control` removes only the idempotency guard. That control matters: the same RecoveryLab scenario then reports two writes and two different IDs. The Spring project already has its own payment endpoint and ledger; the [read-only count adapter](experiments/ledger-count.py) converts its ledger array to an integer for RecoveryLab.

I also ran one [Express](experiments/idempotency/express-result.json), [FastAPI](experiments/idempotency/fastapi-result.json), and [Spring](experiments/idempotency/spring-result.json) probe before the batches. The fresh [Express control](experiments/idempotency/express-control-result.json) and [FastAPI control](experiments/idempotency/fastapi-control-result.json) reports show `0 → 2` where `0 → 1` was expected. The batch controls started after those probes, so their first baseline is 2 rather than 0. RecoveryLab uses `baseline + expected_delta`, so this does not change the verdict. After the guarded batches, I fetched `/state` directly from each small host; both returned `count: 10` from a fresh process. Every report records the fault injection, upstream and retry HTTP codes, JSON field check, final count, and unique run ID.

## Reproduce it

Clone the three repos and check out the exact commits linked above. For the Express project, run `npm ci --ignore-scripts --no-audit` and compile it with `node node_modules/typescript/bin/tsc --build tsconfig.dist.json`. Its `npm run compile` script uses a Unix-style executable path, so I called the compiler directly on Windows. Then, from this repository's root, start:

```powershell
node docs/experiments/idempotency/express-server.cjs C:\path\to\express-idempotency 18080 guard
```

In another terminal, run:

```powershell
recoverylab run --repeat 10 docs/experiments/idempotency/express-lost-response.json
```

Stop the host and restart it with `control` in place of `guard`; rerun the same scenario. The host starts with an empty counter each time. This test used Node 20.17.0 and npm 10.8.2. `npm ci` warned that one dev dependency wanted a newer Node 20 patch, but installation and compilation succeeded.

For idemptx, use a Python 3.12 virtual environment and install its checkout in editable mode along with `fastapi==0.141.1`, `redis==5.3.1`, `pydantic==2.13.5`, and `uvicorn==0.53.0`. From `docs/experiments/idempotency`, start `python -m uvicorn fastapi_server:app --host 127.0.0.1 --port 18081`, using the virtual environment's Python. In the repo root, run `recoverylab run --repeat 10 docs/experiments/idempotency/fastapi-lost-response.json`. To run the control, stop the server, set `IDEMPOTENCY_MODE=control`, restart it, and use the same scenario. This check used idemptx's documented **in-memory** backend, not Redis.

For Spring, follow the [sample-app setup](external-checks.md#spring-sample-setup) with Java 25 and the count adapter on ports 8080 and 8091. Use [this v0.2.0 scenario](experiments/idempotency/spring-lost-response.json), which adds `/paymentId` comparison. After the ten-run batch I fetched `GET /ledger` directly: it held 11 entries, one from the first probe plus ten from the batch.

## What this does and does not establish

These are contract checks of three specific, pinned integrations. They show that RecoveryLab can verify a lost reply, one handler execution per key, and a selected ID in the replay across Express, FastAPI, and Spring. The controls show that the same scenarios detect missing idempotency in the two small hosts. **I found no bug in these upstream projects.**

The hosts and Spring sample keep state in memory. These runs say nothing about a process crash, a separate database, multiple service instances, TTL expiry, or a real payment provider. RecoveryLab compared only the selected ID and the count; it did not compare every response field. A 10-run batch is repeatability evidence for this setup, not a reliability guarantee. The [raw reports](experiments/idempotency) are included so the exact observations can be checked without relying on this summary.
