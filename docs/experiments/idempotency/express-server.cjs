// A small host for the unmodified express-idempotency library at a pinned checkout.
// Usage: node express-server.cjs UPSTREAM_REPO [port] [guard|control]
const { createRequire } = require('node:module');
const path = require('node:path');

const upstream = process.argv[2];
const port = Number(process.argv[3] || 18080);
const mode = process.argv[4] || 'guard';
const delayMs = Number(process.env.OPERATION_DELAY_MS || 0);
if (!upstream || !Number.isInteger(port) || port < 1 || port > 65535 || !['guard', 'control'].includes(mode) || !Number.isInteger(delayMs) || delayMs < 0 || delayMs > 10000) {
  console.error('usage: node express-server.cjs UPSTREAM_REPO [port] [guard|control]');
  process.exit(2);
}

const fromUpstream = createRequire(path.join(path.resolve(upstream), 'package.json'));
const express = fromUpstream('express');
const { idempotency, getSharedIdempotencyService } = fromUpstream('./dist/index.js');
const app = express();
app.use(express.json());

const middleware = idempotency();
const service = getSharedIdempotencyService();
let count = 0;

app.get('/state', (_req, res) => res.json({ count }));
const handler = (req, res) => {
  if (mode === 'guard' && service.isHit(req)) return;
  const complete = () => {
    count += 1;
    res.status(201).json({ operation_id: `op-${count}`, amount: req.body.amount });
  };
  if (delayMs) setTimeout(complete, delayMs);
  else complete();
};
if (mode === 'guard') app.post('/operations', middleware, handler);
else app.post('/operations', handler);

app.listen(port, '127.0.0.1', () => {
  console.log(`express idempotency ${mode} listening on 127.0.0.1:${port}`);
});
