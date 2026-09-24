# Retry after a process crash

The original `crash-after-ack` check asked whether an acknowledged write survived a process restart. It now has an optional next step: send the **same request** after restart and check its reply and the state again. RecoveryLab reads state before the retry too. A later retry cannot hide an acknowledged write that disappeared during the crash.

## A third-party check

I tested this against [PocketBase v0.40.4](https://github.com/pocketbase/pocketbase/releases/tag/v0.40.4) on Windows with a disposable local database. The write used PocketBase's ordinary public record-create route and the [small collection migration](experiments/pocketbase-collection.js) from the earlier check. RecoveryLab started PocketBase directly, created a record, received HTTP 200, killed the process, restarted it, and sent the same JSON request again. The client key in the body stayed the same.

The [raw report](experiments/pocketbase-crash-replay-result.json) records this result:

| Point in the run | Record count |
| --- | ---: |
| Before the first request | 0 |
| After restart, before retry | 1 |
| After retry | 2 |

Both responses were HTTP 200, but their `/id` values differed. RecoveryLab returned `VIOLATION`: the first write survived, and the retry created another record. **This is not a PocketBase bug report.** That ordinary route does not promise to deduplicate creates by `clientKey`. The check shows what happens when a caller needs one effect across a crash and retry.

To repeat it, use the verified PocketBase v0.40.4 binary and setup in [the earlier PocketBase notes](external-checks.md#pocketbase-setup). Put the migration in the extracted app's `pb_migrations` directory as `202609240000_create_retry_orders.js`. Copy [this scenario template](experiments/pocketbase-crash-replay.template.json), change `service.working_dir` to the extracted app directory, and run it with the new RecoveryLab binary. The template uses port 18090 and a separate `pb_data_crash_check` directory. Change `./pocketbase.exe` to `./pocketbase` on macOS or Linux. Do not start PocketBase yourself; RecoveryLab manages the process for this scenario.

```powershell
recoverylab validate pocketbase-crash-replay.json
recoverylab run pocketbase-crash-replay.json
```

The result applies to that local route and database. It does not test a database crash, a machine restart, multiple instances, or any promise of PocketBase idempotency.

## Controls in the included fixture

The [working scenario](../examples/crash-retry.json) uses RecoveryLab's persistent journal fixture. It returns the same `/operation_id` after restart and keeps one effect. I also ran it with two deliberate bug modes on fresh journals:

| Fixture mode | State before retry | State after retry | Reply | Verdict |
| --- | ---: | ---: | --- | --- |
| `correct` | 1 | 1 | Same ID | `PASS` |
| `replay-bug` | 1 | 1 | Changed ID | `VIOLATION` |
| `repair-after-crash-bug` | 0 | 1 | Same ID | `VIOLATION` |

The last case is why the before-retry observation is necessary. Its final count looks right only because the retry applied a write that the first acknowledged request never made durable. These fixture modes test RecoveryLab's verdict logic; they are not findings in an external service.
