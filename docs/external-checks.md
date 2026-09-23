# Checks against projects we didn't build

On 24 September 2026, I ran the published [RecoveryLab v0.1.2](https://github.com/Wali05/recoverylab/releases/tag/v0.1.2) Windows binary against two unrelated open-source projects. I verified its release checksum before running it. Both services and their data stayed on my machine.

| Service and request | Result | RecoveryLab verdict |
| --- | --- | --- |
| [PocketBase v0.40.4](https://github.com/pocketbase/pocketbase/releases/tag/v0.40.4): create a record, lose the reply, retry | Record count `0 → 2`; the scenario expected `1` | `VIOLATION`, exit 1 |
| PocketBase: send 8 copies of a create request together | Record count `2 → 10`; the scenario expected `3` | `VIOLATION`, exit 1 |
| [Spring idempotency sample at `9346b65`](https://github.com/arthurfaby/spring-boot-starter-idempotency/tree/9346b655710ebda5c2bae762c2735a1fd3b18ec2): create a payment, lose the reply, retry with the same key | Ledger count `0 → 1`; the scenario expected `1` | `PASS`, exit 0 |
| Spring sample: 16 copies with the same key, repeated in 3 separate runs | Each run added one ledger entry; all 3 runs passed | 3 `PASS`, 0 violations |

The key lines from the two lost-response runs were:

```text
PocketBase: upstream HTTP 200; client saw a lost response; retry HTTP 200; expected 1, observed 2
Spring sample: upstream HTTP 201; client saw a lost response; retry HTTP 201; expected 1, observed 1
```

The PocketBase results are **not PocketBase bug reports**. Its ordinary record-create route does not claim to deduplicate these requests. The scenario deliberately asks, “What if a client expected one record?” and RecoveryLab shows the mismatch. The Spring sample is built to handle duplicate payment requests with an `Idempotency-Key`, and this setup kept one ledger entry per key.

## PocketBase setup

I downloaded the official Windows AMD64 archive and `checksums.txt` for v0.40.4. The archive's SHA-256 matched `81c30964508fa7df15fe4571a6ee39e059859c566dad03ebf181aebe07366c3e`.

I extracted it to a temporary directory and put [this migration](experiments/pocketbase-collection.js) in `pb_migrations`. The migration creates one public `retry_orders` collection with a required `clientKey` text field. It changes the **disposable local database schema**, not PocketBase's source. From that directory, I started PocketBase on a free loopback port with a separate `pb_data` directory and ran [the lost-response scenario](experiments/pocketbase-lost-response.json) and [the eight-request scenario](experiments/pocketbase-concurrent.json). The checked value came straight from PocketBase's `GET /api/collections/retry_orders/records?perPage=1` response at `/totalItems`; no observation adapter was needed.

To repeat the check, use port `8090` as in the scenario files, or change both URLs to the port you choose:

```powershell
# In the extracted PocketBase directory, after copying the migration:
.\pocketbase.exe serve --http=127.0.0.1:8090 --dir=./pb_data --migrationsDir=./pb_migrations

# In the RecoveryLab repo, in another terminal:
recoverylab validate docs/experiments/pocketbase-lost-response.json
recoverylab run docs/experiments/pocketbase-lost-response.json
recoverylab run docs/experiments/pocketbase-concurrent.json
```

Run the two scenarios in that order on a fresh database to get the starting counts in the table. PocketBase's [record API](https://pocketbase.io/docs/api-records/) documents the create and list routes, including `totalItems`.

## Spring sample setup

I cloned the [idempotency starter](https://github.com/arthurfaby/spring-boot-starter-idempotency) at commit `9346b655710ebda5c2bae762c2735a1fd3b18ec2`, built its `sample-app` module with Maven and Java 25, and ran the sample jar on loopback. I did not edit the sample app. It exposes `POST /payments` and `GET /ledger`. The latter returns an array, while RecoveryLab currently reads integer JSON values, so [this read-only adapter](experiments/ledger-count.py) serves `{"count": <length of /ledger>}` on another loopback port. I checked the final ledger directly too: it contained one entry after the lost-response run and four after the three concurrent runs.

```powershell
# In the cloned starter repo, checked out at the commit above:
.\mvnw.cmd -pl sample-app -am -DskipTests package
java -jar sample-app/target/sample-app-0.1.2.jar --server.address=127.0.0.1 --server.port=8080

# In the RecoveryLab repo, in a separate terminal:
python docs/experiments/ledger-count.py 8080 8091

# In another terminal:
recoverylab validate docs/experiments/spring-lost-response.json
recoverylab run docs/experiments/spring-lost-response.json
recoverylab run --repeat 3 docs/experiments/spring-concurrent.json
```

The [sample controller](https://github.com/arthurfaby/spring-boot-starter-idempotency/blob/9346b655710ebda5c2bae762c2735a1fd3b18ec2/sample-app/src/main/java/com/arthurfaby/idempotency/sample/PaymentController.java) stores its ledger in memory. These runs test lost responses and concurrent duplicates **while that process stays alive**. They do not test its durability after a crash. A `PASS` also checks only the declared ledger count; it does not certify every part of the payment response or application behavior.

The earlier [NetCore check](independent-check.md) is useful too, but NetCore is my own project. These two checks give us evidence from code maintained by other people. They still do not prove RecoveryLab works with every HTTP service or every failure mode.
