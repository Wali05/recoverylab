# Fixing what RecoveryLab finds

Every finding RecoveryLab reports links to one of the sections below. They describe one design, the one most payment APIs use, and how each part prevents one kind of failure. The examples use PostgreSQL and Go, but the idea carries over to any database with a unique constraint and transactions.

- [The design in one table](#the-design-in-one-table)
- [Store the key with the effect](#store-the-key-with-the-effect)
- [Claim the key atomically](#claim-the-key-atomically)
- [Replay the first response](#replay-the-first-response)
- [Bind the key to the request](#bind-the-key-to-the-request)
- [Reply after the write commits](#reply-after-the-write-commits)
- [Keep keys across restarts](#keep-keys-across-restarts)
- [Commit every effect together](#commit-every-effect-together)
- [Don't leave a key claimed by a dead request](#dont-leave-a-key-claimed-by-a-dead-request)
- [Let old keys expire](#let-old-keys-expire)
- [When the work can't fit in one transaction](#when-the-work-cant-fit-in-one-transaction)

## The design in one table

The client sends a unique `Idempotency-Key` with each operation and reuses it only when retrying that same operation. The server keeps one row per key, next to the data the operation changes:

```sql
CREATE TABLE idempotency_keys (
  scope       text        NOT NULL,  -- the account or user the key belongs to
  key         text        NOT NULL,
  fingerprint text        NOT NULL,  -- hash of method, path, and body
  status      int         NOT NULL,
  response    bytea       NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, key)
);
```

Scope the key to the caller, so two customers who happen to pick the same key don't see each other's results.

## Store the key with the effect

*Fixes: a retry after a lost response creates a second order.*

The key and the order must be written in the same transaction. If the order commits but the key doesn't, the retry runs again. If the key commits but the order doesn't, the retry is told the work happened when it didn't. AWS calls out exactly this: recording the token and doing the work have to succeed or fail together ([AWS Builders' Library](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-apis/)).

```go
func createOrder(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path + "\n" + string(body)))
	fingerprint := hex.EncodeToString(sum[:])
	scope := accountID(r)

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback()

	// Claim the key. A concurrent request with the same key waits here
	// until this transaction commits or rolls back.
	res, err := tx.Exec(`INSERT INTO idempotency_keys (scope, key, fingerprint, status, response)
		VALUES ($1, $2, $3, 0, '') ON CONFLICT DO NOTHING`, scope, key, fingerprint)
	if err != nil {
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	if claimed, _ := res.RowsAffected(); claimed == 0 {
		replay(w, tx, scope, key, fingerprint)
		return
	}

	order, err := insertOrder(tx, body) // the real work, in the same transaction
	if err != nil {
		http.Error(w, "invalid order", http.StatusUnprocessableEntity)
		return // rolling back releases the key, so a corrected retry can use it
	}
	reply, _ := json.Marshal(order)
	if _, err := tx.Exec(`UPDATE idempotency_keys SET status = 201, response = $3
		WHERE scope = $1 AND key = $2`, scope, key, reply); err != nil {
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(reply)
}
```

## Claim the key atomically

*Fixes: a burst of identical requests creates several orders, or the extra copies get HTTP 500.*

A lookup followed by an insert ("have I seen this key? no, so do the work") is a race: every request in a burst can pass the lookup before any of them writes. The unique primary key and `INSERT ... ON CONFLICT DO NOTHING` above make the claim a single step. In PostgreSQL, a second transaction inserting the same key waits for the first to finish, then sees the conflict and replays the stored result.

Don't let the database's duplicate-key error escape as a 500. Clients retry 500s, which feeds more copies into the same race. If a request can't wait for the one in progress, answer **409 Conflict**. That's what the [IETF Idempotency-Key draft](https://datatracker.ietf.org/doc/html/draft-ietf-httpapi-idempotency-key-header) specifies for a retry that arrives before the original finishes. [Stripe](https://docs.stripe.com/idempotency) also lets a request that conflicts with one still running be retried. Declare the codes your API promises in the scenario with `"concurrent_response": { "allowed_statuses": [201, 409] }`.

## Replay the first response

*Fixes: the retry gets 409 or 500 although the write happened, or the retry returns a different ID.*

A retry should get the same answer the lost reply would have given. "Already exists" is not enough. The client can't tell whether it created the order or someone else did, and it still doesn't have the order's ID. AWS recommends returning a response equivalent to the original for the same token, and [Stripe](https://docs.stripe.com/idempotency) stores the first status code and body for each key, errors included, and returns them for every repeat.

```go
func replay(w http.ResponseWriter, tx *sql.Tx, scope, key, fingerprint string) {
	var saved string
	var status int
	var response []byte
	err := tx.QueryRow(`SELECT fingerprint, status, response FROM idempotency_keys
		WHERE scope = $1 AND key = $2`, scope, key).Scan(&saved, &status, &response)
	if err != nil {
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	if saved != fingerprint {
		http.Error(w, "Idempotency-Key was used for a different request", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status) // the original 201, not a 200 or a 409
	w.Write(response)
}
```

RecoveryLab's `audit` compares ID fields such as `id` and `order_id` between the original reply and the retry. Fields that legitimately change per request, such as `request_id`, are ignored.

## Bind the key to the request

*Fixes: reusing a key with a different body creates a second order, or quietly returns the first order.*

A key identifies one operation. If a client bug sends a different body with an old key, neither running it nor replaying the old result is right. The fingerprint stored with the key catches this, and the server answers **422** without writing anything. The IETF draft uses 422 for this case and Stripe compares the parameters of each repeat with the original. Include everything that changes the meaning of the request in the fingerprint: method, path, and body.

## Reply after the write commits

*Fixes: the service said 201, then lost the order when it crashed.*

Send the success response only after the transaction commits. A write held in memory, queued for a background job, or still uncommitted when the 201 goes out will be lost if the process dies at the wrong moment, and the client will never try again because it was told the order exists.

If the work really has to happen later, say so: answer **202 Accepted** with a link the client can poll, and make the state endpoint count accepted work.

## Keep keys across restarts

*Fixes: the order survived the crash, but retrying after restart created another one.*

Keys kept in a map, an in-process cache, or a single Redis instance without persistence vanish with the process. Store them in the same database as the effect, which the table above does.

## Commit every effect together

*Fixes: a crash mid-request left an order without its charge.*

One request often changes several things: an order row, a ledger entry, a stock count. If they are written one after another, each in its own transaction, the process can die between any two of them. The order then exists without its ledger entry, and no later request will notice.

Write them in one transaction. The handler in [Store the key with the effect](#store-the-key-with-the-effect) already does this: `insertOrder` takes the same `tx` that claimed the key, and anything else the request changes should take it too.

```go
order, err := insertOrder(tx, body)
if err != nil { ... }
if err := insertLedgerEntry(tx, order); err != nil { ... } // same tx: both or neither
```

To see this in a test, give the scenario one check per effect. RecoveryLab's `crash-mid-request` kills the service part-way through the request and, before it retries, requires every check to show the write either done or not done. One that moved while another didn't is a half-finished write.

## Don't leave a key claimed by a dead request

*Fixes: after a crash mid-request, the retry is told the work is done when it isn't, or is refused forever.*

A key that is saved before its work, in a write of its own, can outlive the request that saved it. After a crash the service finds the key and has two bad options. If it treats the key as finished, it replies "done" for work that never happened. If it treats the key as in progress, it answers 409 to every retry, waiting for a request that is gone.

The fix is the same transaction again. When the key and the work commit together, a crash rolls both back and the retry starts clean. In PostgreSQL the claim in the handler above is exactly that: the `INSERT` into `idempotency_keys` isn't visible to anyone until the order commits with it.

If the key really must be claimed first, because the work calls another system, store who claimed it and when, and let a retry take over a claim that is older than any live request could be:

```sql
UPDATE idempotency_keys SET locked_at = now()
WHERE scope = $1 AND key = $2 AND status = 0 AND locked_at < now() - interval '30 seconds';
```

One row updated means this retry now owns the key and can redo the work. Brandur Leach's [Stripe-like design](https://brandur.org/idempotency-keys) uses the same idea: a `locked_at` column, plus recovery points that record how far the first attempt got.

## Let old keys expire

Keys don't need to live forever. [Stripe](https://docs.stripe.com/idempotency) removes keys once they're at least 24 hours old, after which reusing a key starts a new request. Clean them up on a schedule, for example `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`, and keep the window longer than the longest retry your clients do.

## When the work can't fit in one transaction

Calling a payment provider or sending an email can't be rolled back with your database. For those, store the key first with an in-progress state, do each external step, and record progress after each one, so a retry can resume instead of starting over. Brandur Leach describes this in detail in [Implementing Stripe-like Idempotency Keys in Postgres](https://brandur.org/idempotency-keys). Pass your own idempotency key to the provider too, if it accepts one, so its side of the work is also safe to repeat.

## Further reading

- [Making retries safe with idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-apis/), AWS Builders' Library
- [Idempotent requests](https://docs.stripe.com/idempotency), Stripe API documentation
- [The Idempotency-Key HTTP Header Field](https://datatracker.ietf.org/doc/html/draft-ietf-httpapi-idempotency-key-header), IETF Internet-Draft
- [Implementing Stripe-like Idempotency Keys in Postgres](https://brandur.org/idempotency-keys), Brandur Leach
