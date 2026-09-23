# Twenty more API checks

On 24 September 2026, I ran the published RecoveryLab v0.1.2 Windows binary against 20 open-source API repos I do not maintain. These are mostly small FastAPI CRUD projects. I cloned each repo at the linked commit, ran its service on loopback, and used RecoveryLab's `lost-response` scenario on one write endpoint. The [run data](experiments/compatibility-results.json) has the request body, setup step if one was needed, observation path, commit, HTTP statuses, and counts for every row.

The check asks one narrow question: **if a successful write's response disappears and the same request is retried, does the observed count rise by one?** For 15 services it rose by two. For five it rose by one, but their retries returned an error (400, 422, or 500). RecoveryLab calls those five `PASS` because its declared count matched. That does **not** mean those APIs gave the client a usable retry response.

| Repo at tested commit | Write | Read | Count: before / expected / observed | Retry HTTP | State verdict |
| --- | --- | --- | --- | ---: | --- |
| [Anissa667u/manuscripts-api](https://github.com/Anissa667u/manuscripts-api/tree/3f8abe77a44956e44677aeada19c9879c3975e3b) | `/manuscripts/` | `/manuscripts/` | 7 / 8 / 9 | 200 | VIOLATION |
| [Ardajs/mini-books-api](https://github.com/Ardajs/mini-books-api/tree/04b1580e8288d479553d1857af4c80a73f4c58c5) | `/book/add` | `/books` | 0 / 1 / 2 | 200 | VIOLATION |
| [BpsEason/fastapi-message-board](https://github.com/BpsEason/fastapi-message-board/tree/32f5c658953616da8d814bdbb568e4b698746858) | `/messages/` | `/messages/` | 0 / 1 / 2 | 200 | VIOLATION |
| [Caiov7003/api-produtos](https://github.com/Caiov7003/api-produtos/tree/7c047f86621559e968a67e570fedf1b84dd7e485) | `/produtos` | `/produtos` | 0 / 1 / 2 | 201 | VIOLATION |
| [Chelsea-yang-xi/hotel-booking-api](https://github.com/Chelsea-yang-xi/hotel-booking-api/tree/d2f747dd88c156a6c25a90a9dede0f1fad5ea78d) | `/hotels` | `/hotels` | 0 / 1 / 2 | 200 | VIOLATION |
| [damianrydzewski/FastAPI-SQLModel-CRUD-app](https://github.com/damianrydzewski/FastAPI-SQLModel-CRUD-app/tree/ce1abaa4203cedec76313783db04e23e358e3495) | `/heroes/` | `/heroes/` | 0 / 1 / 2 | 200 | VIOLATION |
| [dhruvbadhe/crud-app](https://github.com/dhruvbadhe/crud-app/tree/90f0e56d8c29007103a63c490d605c0ca4c2f0b3) | `/employees/` | `/employees/` | 0 / 1 / 1 | 500 | PASS |
| [FidelCoder7/personal-expense-tracker](https://github.com/FidelCoder7/personal-expense-tracker/tree/17c576f3af64a8e113a583024853e43bcd11efa9) | `/transactions/` | `/transactions/` | 0 / 1 / 2 | 201 | VIOLATION |
| [HR-Builds/FastAPI-CRUD-Patient-Management](https://github.com/HR-Builds/FastAPI-CRUD-Patient-Management/tree/2b6641ef6fcbeb0ef86bdafee29ed945afd28609) | `/patients` | `/patients` | 0 / 1 / 2 | 200 | VIOLATION |
| [ImedBousakhria/fastapi_Sql_project](https://github.com/ImedBousakhria/fastapi_Sql_project/tree/f08f4cfc7e91cde0eeb3fdf11607d789c2a7c725) | `/todos/add_todo` | `/todos` | 2 / 3 / 3 | 500 | PASS |
| [jessicasalestech/python-fastapi-inventory-api](https://github.com/jessicasalestech/python-fastapi-inventory-api/tree/c948964572cc378b7c5e12333b5dd47b82a422e8) | `/items` | `/items` | 0 / 1 / 2 | 201 | VIOLATION |
| [judeabii/PythonAPI-CRUD-Operations](https://github.com/judeabii/PythonAPI-CRUD-Operations/tree/d13c239f4cc566f8acfa52dc30494a56ad359171) | `/student` | `/` | 12 / 13 / 14 | 201 | VIOLATION |
| [lymanny/FastAPI-CRUD-Todo](https://github.com/lymanny/FastAPI-CRUD-Todo/tree/0ccc618b60c73bbc9f7a488a213ea14e852cb776) | `/todos/` | `/todos/` | 0 / 1 / 2 | 200 | VIOLATION |
| [mqqikq/flashcards-api](https://github.com/mqqikq/flashcards-api/tree/894ff4a281d4f281da4b2aae818888c2da06ab14) | `/decks/{deck_id}/cards` | `/stats` | 0 / 1 / 2 | 201 | VIOLATION |
| [okashaghaffar/FastApi-boilerplat-Crud](https://github.com/okashaghaffar/FastApi-boilerplat-Crud/tree/7a6631faa03188d40f33ccfc77421c44de2b4135) | `/users/` | `/` | 0 / 1 / 1 | 400 | PASS |
| [pedro-guedes-data/FastAPI_CRUD](https://github.com/pedro-guedes-data/FastAPI_CRUD/tree/67a449eeef2682ad66c966753c194446e31a2c35) | `/pets` | `/pets` | 5 / 6 / 6 | 422 | PASS |
| [r-otavio-dev/task-api](https://github.com/r-otavio-dev/task-api/tree/04d1efa3ab3fc7364bac25cbc5251554598d61d7) | `/tasks` | `/tasks` | 3 / 4 / 5 | 201 | VIOLATION |
| [rrslamerr/dating-profile-manager](https://github.com/rrslamerr/dating-profile-manager/tree/5e468e4268a9fe455dcaa7d981b94f71043c951a) | `/profiles/` | `/profiles/` | 0 / 1 / 2 | 200 | VIOLATION |
| [Sanny0615/student-management-api](https://github.com/Sanny0615/student-management-api/tree/02e8c173217cd03554856f277a8686225fafa191) | `/students` | `/students/0` | 0 / 1 / 1 | 400 | PASS |
| [th-santos/python-backend-sample](https://github.com/th-santos/python-backend-sample/tree/f50c1a92eaf4340aef151482395b0b952098072e) | `/items` | `/items` | 0 / 1 / 2 | 200 | VIOLATION |

## How I checked the counts

I used Python 3.12.6 with FastAPI 0.111.0, Uvicorn 0.30.1, Pydantic 2.7.1, and SQLAlchemy 2.0.30; two apps needed extra packages. I started each checkout with `python -m uvicorn <module>:app --host 127.0.0.1 --port <free-port>`. The module, working directory, environment override, setup request, and exact write body for each app are in the [run data](experiments/compatibility-results.json). Some repositories include existing SQLite data, which explains baselines above zero. The flashcards API needed one deck created before the test.

RecoveryLab recorded the original upstream status, dropped that response, sent the retry, and compared the final count with `before + 1`. When the app exposed a JSON array rather than an integer, a read-only [array-count adapter](experiments/array-count.py) turned the array length into `{"count": N}` on another loopback port. I then fetched the app's read endpoint directly and checked its array length against RecoveryLab's reported final value. The flashcards app exposed an integer at `/stats`, so it needed no adapter. All 20 direct checks agreed with RecoveryLab.

These were disposable local runs. I did not change the applications' source code. I excluded a separate candidate that returned HTTP 500 during setup because RecoveryLab never reached a verdict on it.

## What these results mean

`VIOLATION` means the final **declared count** missed the test's expectation of one new record. It is not an assertion that a project violated its own API contract: most of these endpoints do not claim to deduplicate retries. Likewise, a count `PASS` can coexist with a failed retry response. Two of the five `PASS` rows retried with HTTP 500.

This batch checks one endpoint and one lost-response run per repo. It does not cover authentication, concurrent requests, crash recovery, other state changes, or every response field. These 20 repos are heavily weighted toward small Python CRUD examples, so the number is evidence that the tool connected to a range of real services, not proof of universal compatibility. I also ran [27 checks across Flask, Express, and Spring](compatibility-checks-2.md). The separate [PocketBase and Spring checks](external-checks.md) include concurrent-request runs.
