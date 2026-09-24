"""A small host for the unmodified idemptx library at a pinned checkout.

Run with: python -m uvicorn fastapi_server:app --host 127.0.0.1 --port 18081
Set IDEMPOTENCY_MODE=control to run the same endpoint without the decorator.
"""

import os
import asyncio

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from idemptx.backend.memory import InMemoryBackend
from idemptx.decorator import idempotent
from idemptx.exceptions import IdempotencyConflictException

app = FastAPI()
backend = InMemoryBackend()
count = 0
delay_ms = int(os.environ.get('OPERATION_DELAY_MS', '0'))
if delay_ms < 0 or delay_ms > 10000:
    raise ValueError('OPERATION_DELAY_MS must be between 0 and 10000')


@app.exception_handler(IdempotencyConflictException)
async def conflict(_request: Request, exc: IdempotencyConflictException):
    return JSONResponse(status_code=409, content={'detail': str(exc)})


@app.get('/state')
async def state():
    return {'count': count}


async def create_operation(request: Request):
    global count
    payload = await request.json()
    if delay_ms:
        await asyncio.sleep(delay_ms / 1000)
    count += 1
    return JSONResponse(
        status_code=201,
        content={'operation_id': f'op-{count}', 'amount': payload['amount']},
    )


if os.environ.get('IDEMPOTENCY_MODE') == 'control':
    app.post('/operations')(create_operation)
else:
    app.post('/operations')(idempotent(storage_backend=backend)(create_operation))
