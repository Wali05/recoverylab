# Check against a separate service

On 23 September 2026, RecoveryLab was run against [NetCore at commit `e299c45`](https://github.com/Wali05/NetCore/tree/e299c45). NetCore is a separate Spring Boot IP address management application. This check used its local, in-memory H2 profile; it did not modify NetCore's source or a persistent database.

The setup started a clean NetCore process on a free loopback port, then made these requests:

| Request | JSON body |
| --- | --- |
| `POST /api/v1/subnets` | `{"networkAddress":"10.245.42.0","prefixLength":29,"gatewayAddress":"10.245.42.1"}` |
| `POST /api/v1/devices` | `{"name":"retry-check","type":"ROUTER"}` |
| `POST /api/v1/devices/{deviceId}/interfaces` | `{"name":"eth0","macAddress":"02:00:00:00:00:42"}` |

The subnet initially had zero allocated addresses. RecoveryLab then tested `POST /api/v1/subnets/{id}/allocate-next` with the `lost-response` scenario. It read `/totalElements` from NetCore's `GET /api/v1/subnets/{id}/addresses?status=ALLOCATED` response and expected one new allocation.

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

This confirms that RecoveryLab can run the lost-response experiment against another HTTP application and detect a state change after a retry. It also shows that this particular NetCore endpoint allocated two addresses in this setup. The endpoint does not promise idempotency, so the result is **not** evidence that NetCore broke its documented contract. It is a concrete example of the behavior a client must account for when retrying an allocation after an ambiguous response.
