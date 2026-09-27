// Small test host for idempot-dev/idempot-js. Run only with a trusted checkout.
import { randomUUID } from "node:crypto";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const [upstreamArg, databaseArg, portArg, mode = "guard"] = process.argv.slice(2);
if (!upstreamArg || !databaseArg || !portArg || !["guard", "control"].includes(mode)) {
  console.error("usage: node idempot-js-sqlite-host.mjs UPSTREAM_DIR DATABASE_PATH PORT [guard|control]");
  process.exit(2);
}

const upstream = resolve(upstreamArg);
const storeModule = await import(pathToFileURL(resolve(upstream, "packages/stores/sqlite/index.js")).href);
const middlewareModule = await import(pathToFileURL(resolve(upstream, "packages/frameworks/hono/index.js")).href);
const honoRequire = createRequire(resolve(upstream, "packages/frameworks/hono/package.json"));
const { Hono } = await import(pathToFileURL(honoRequire.resolve("hono")).href);
const store = new storeModule.SqliteIdempotencyStore({ path: resolve(databaseArg) });

store.db.exec("CREATE TABLE IF NOT EXISTS orders (id TEXT PRIMARY KEY, item TEXT NOT NULL)");
const countOrders = store.db.prepare("SELECT COUNT(*) AS count FROM orders");
const insertOrder = store.db.prepare("INSERT INTO orders (id, item) VALUES (?, ?)");

const app = new Hono();
app.get("/health", (c) => c.json({ ready: true }));
app.get("/state", (c) => c.json({ count: countOrders.get().count }));
const createOrder = async (c) => {
  const body = await c.req.json();
  const id = randomUUID();
  insertOrder.run(id, body.item);
  return c.json({ id }, 201);
};
if (mode === "guard") {
  app.post("/orders", middlewareModule.idempotency({ store }), createOrder);
} else {
  app.post("/orders", createOrder);
}

const server = createServer(async (incoming, outgoing) => {
  try {
    const body = [];
    for await (const chunk of incoming) body.push(chunk);
    const request = new Request(`http://127.0.0.1:${portArg}${incoming.url}`, {
      method: incoming.method,
      headers: incoming.headers,
      body: body.length ? Buffer.concat(body) : undefined
    });
    const response = await app.fetch(request);
    outgoing.writeHead(response.status, Object.fromEntries(response.headers));
    outgoing.end(Buffer.from(await response.arrayBuffer()));
  } catch (error) {
    console.error(error);
    if (!outgoing.headersSent) outgoing.writeHead(500);
    outgoing.end("test host error");
  }
});
server.listen(Number(portArg), "127.0.0.1");
