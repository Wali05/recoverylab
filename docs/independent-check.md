# Check against a separate service

On 23 September 2026, I ran RecoveryLab against [NetCore at commit `e299c45`](https://github.com/Wali05/NetCore/tree/e299c45), a separate Spring Boot app for managing IP addresses. NetCore used its local, in-memory H2 database. I did not change its code or any persistent data.

I started a fresh NetCore process on a free loopback port and made these requests:

| Request | JSON body |
| --- | --- |
| `POST /api/v1/subnets` | `{"networkAddress":"10.245.42.0","prefixLength":29,"gatewayAddress":"10.245.42.1"}` |
| `POST /api/v1/devices` | `{"name":"retry-check","type":"ROUTER"}` |
| `POST /api/v1/devices/{deviceId}/interfaces` | `{"name":"eth0","macAddress":"02:00:00:00:00:42"}` |

The subnet started with zero allocated addresses. RecoveryLab then tested `POST /api/v1/subnets/{id}/allocate-next` with the `lost-response` scenario. It read `/totalElements` from NetCore's `GET /api/v1/subnets/{id}/addresses?status=ALLOCATED` response and expected one new allocation.

The scenario was equivalent to this JSON, with the loopback port and IDs filled in from the running service:

```json
{
  "name": "netcore-allocate-next-lost-response",
  "scenario": "lost-response",
  "timeout": "20s",
  "request": {
    "method": "POST",
    "url": "http://127.0.0.1:PORT/api/v1/subnets/SUBNET_ID/allocate-next",
    "headers": { "Content-Type": "application/json" },
    "body": { "networkInterfaceId": 1 }
  },
  "observe": {
    "url": "http://127.0.0.1:PORT/api/v1/subnets/SUBNET_ID/addresses?status=ALLOCATED",
    "pointer": "/totalElements",
    "expected_delta": 1
  }
}
```

Replace `PORT`, `SUBNET_ID`, and `1` with the port and IDs returned during setup, save the result as a scenario file, and run `recoverylab run scenario.json`. The tested request sent no idempotency key.

| Observation | Result |
| --- | --- |
| Allocated addresses before | 0 |
| First request | NetCore returned HTTP 200 to the proxy; the client saw a lost reply |
| Retried request | HTTP 200 |
| Allocated addresses after | 2 |
| RecoveryLab verdict | `VIOLATION`, exit code 1; expected final count 1 |

RecoveryLab detected the extra allocation in a separate HTTP app. NetCore does not promise idempotency for this endpoint, so two allocations do not break its documented contract. The result shows what a client can run into if it retries after losing the first response.
