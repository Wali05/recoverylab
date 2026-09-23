// Let a cloned demo with a fixed listen port run on a free loopback port.
// Preload with: PORT=8090 RECOVERYLAB_ORIGINAL_PORT=5000 node -r ./port-shim.cjs app.js
const net = require("net");
const original = net.Server.prototype.listen;

net.Server.prototype.listen = function (...args) {
  if (args[0] === Number(process.env.RECOVERYLAB_ORIGINAL_PORT)) {
    args[0] = Number(process.env.PORT);
  }
  return original.apply(this, args);
};
