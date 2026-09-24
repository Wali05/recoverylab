"""A small host for the unmodified idemptx library at a pinned checkout.

Run with: python -m uvicorn fastapi_server:app --host 127.0.0.1 --port 18081
Set IDEMPOTENCY_MODE=control to run the same endpoint without the decorator.
"""

import os

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from idemptx.backend.memory import InMemoryBackend
from idemptx.decorator import idempotent

app = FastAPI()
backend = InMemoryBackend()
count = 0


@app.get('/state')
async def state():
    return {'count': count}


async def create_operation(request: Request):
    global count
    payload = await request.json()
    count += 1
    return JSONResponse(
        status_code=201,
        content={'operation_id': f'op-{count}', 'amount': payload['amount']},
    )


if os.environ.get('IDEMPOTENCY_MODE') == 'control':
    app.post('/operations')(create_operation)
else:
    app.post('/operations')(idempotent(storage_backend=backend)(create_operation))
