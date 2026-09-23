# 27 checks across Flask, Express, and Spring

This is the second batch of checks I ran on 24 September 2026 with the published RecoveryLab v0.1.2 Windows binary. It adds 11 Flask repos, 11 Express repos, and five Spring repos to the [first 20 API checks](compatibility-checks.md). With the earlier [NetCore check](independent-check.md) and [PocketBase and Spring sample checks](external-checks.md), that makes **50 distinct repos checked**.

Each row is one local `lost-response` run: the server processed a write, RecoveryLab dropped its reply, and the same request was retried. I expected the record count to rise by one, then fetched the app's read endpoint directly to check the final count. The [run data](experiments/compatibility-results-part2.json) records each repo's commit, request body, setup, observation path, HTTP statuses, and result.

| Repo at tested commit | Stack | Write | Read | Count: before / expected / observed | Retry HTTP | State verdict |
| --- | --- | --- | --- | --- | ---: | --- |
| [a2rp/express-project-07-simple-crud-api](https://github.com/a2rp/express-project-07-simple-crud-api/tree/53859ce3265fc2ebcd3c026fd2ad1284c7267753) | Express | `/notes` | `/notes` | 2 / 3 / 4 | 201 | VIOLATION |
| [AbohEbim-II/ExpressProductApi](https://github.com/AbohEbim-II/ExpressProductApi/tree/1b0bd4b88315c031a73228b5ece3d5907e526294) | Express | `/products/` | `/products/` | 2 / 3 / 3 | 409 | PASS |
| [alex-arriaga/node-js-restful-api-example](https://github.com/alex-arriaga/node-js-restful-api-example/tree/1758f866ee904c7765265340b9c2723788eb2ee2) | Express | `/events/` | `/events/` | 12 / 13 / 14 | 201 | VIOLATION |
| [amna-mohsin/express-memory-crud](https://github.com/amna-mohsin/express-memory-crud/tree/ce5cb4b35d089ede0fa8471f48007899993376a6) | Express | `/users` | `/users` | 4 / 5 / 6 | 201 | VIOLATION |
| [antoniozgombadev/flask-task-manager-api](https://github.com/antoniozgombadev/flask-task-manager-api/tree/caa3c4bf5da17a253aa6e5cfcce47aab47e6994d) | Flask | `/api/tasks` | `/api/tasks` | 0 / 1 / 2 | 201 | VIOLATION |
| [Anyel-ec/API_REST-Flask-SQLite-Docker](https://github.com/Anyel-ec/API_REST-Flask-SQLite-Docker/tree/9e7e1a834d89536660218ef0a770cee288c3df1c) | Flask | `/productos` | `/productos` | 1 / 2 / 3 | 201 | VIOLATION |
| [AtharvaGhongade/flask-crud-api](https://github.com/AtharvaGhongade/flask-crud-api/tree/17915a812654419e31cb4f7d692aedb4733a1f8b) | Flask | `/books` | `/books` | 0 / 1 / 2 | 201 | VIOLATION |
| [bezkoder/spring-boot-h2-database-crud](https://github.com/bezkoder/spring-boot-h2-database-crud/tree/f02813e2b6c9efc3729663ed783f88db5bb8249e) | Spring | `/api/tutorials` | `/api/tutorials` | 1 / 2 / 3 | 201 | VIOLATION |
| [bezkoder/spring-boot-jdbctemplate-crud-example](https://github.com/bezkoder/spring-boot-jdbctemplate-crud-example/tree/e1c431888c7ff7cd346e0df6946c4dc74f40e947) | Spring | `/api/tutorials` | `/api/tutorials` | 1 / 2 / 3 | 201 | VIOLATION |
| [bezkoder/spring-boot-r2dbc-h2-example](https://github.com/bezkoder/spring-boot-r2dbc-h2-example/tree/eacf4822ac41a97bfbfbf0c5cbc2f50fd2180271) | Spring | `/api/tutorials` | `/api/tutorials` | 0 / 1 / 2 | 201 | VIOLATION |
| [bezkoder/spring-data-rest-example](https://github.com/bezkoder/spring-data-rest-example/tree/4d05d6b43cac97c6459be9f6a382977a483b0d48) | Spring | `/api/tutorials` | `/api/tutorials` | 1 / 2 / 3 | 201 | VIOLATION |
| [codebysaadbouh/-efrei-mdfs-python-alexandry](https://github.com/codebysaadbouh/-efrei-mdfs-python-alexandry/tree/fdd9fe6f81c3c3f805bdece2ab79cc8676b817a5) | Flask | `/books` | `/books` | 0 / 1 / 2 | 201 | VIOLATION |
| [codingott-tech/express-json-products-rest-api](https://github.com/codingott-tech/express-json-products-rest-api/tree/8afb88b5d9c7e0d05a9bc851f19d09f2105cde81) | Express | `/products` | `/products` | 6 / 7 / 8 | 201 | VIOLATION |
| [CryGor11/flask-sqlite-crud-api](https://github.com/CryGor11/flask-sqlite-crud-api/tree/ab68f6b676ed2adce70d1371522147284be4c0fe) | Flask | `/items` | `/items` | 0 / 1 / 2 | 201 | VIOLATION |
| [hasn2022ali/python-curd-api-sqlite](https://github.com/hasn2022ali/python-curd-api-sqlite/tree/daf6524344289f18317d1ea92baf3dbdfd65a322) | Flask | `/api/users` | `/api/users` | 0 / 1 / 1 | 500 | PASS |
| [itsmaheshkariya/flask-rest-api](https://github.com/itsmaheshkariya/flask-rest-api/tree/e65ee8b4c6d0c9fb4395cc07432d0508359f540d) | Flask | `/person` | `/person` | 0 / 1 / 2 | 200 | VIOLATION |
| [j3ny0k/flask-sqlite-todo-api](https://github.com/j3ny0k/flask-sqlite-todo-api/tree/c80d1fb8c26c057140342e6663721f2b28e182a0) | Flask | `/api/tasks` | `/api/tasks` | 0 / 1 / 2 | 201 | VIOLATION |
| [Mansi06Salar/flask-product-rest-api](https://github.com/Mansi06Salar/flask-product-rest-api/tree/42af70191d50f707e936dc44b3d8eab385616831) | Flask | `/products` | `/products` | 0 / 1 / 2 | 201 | VIOLATION |
| [mansiTT/express-crud-api](https://github.com/mansiTT/express-crud-api/tree/60ed046974e5176f7a7c0855000b138d99104eab) | Express | `/user/role` | `/users` | 1 / 2 / 2 | 201 | PASS |
| [matiaswisner/orders-api](https://github.com/matiaswisner/orders-api/tree/680634c85c0ca0b084ff1c40438d21aa65dc3ddb) | Spring | `/api/users` | `/api/users` | 0 / 1 / 1 | 400 | PASS |
| [Ogbodo-Oluebube/student-crud-express-api](https://github.com/Ogbodo-Oluebube/student-crud-express-api/tree/6f42d0b5ada8aad4ad6cfff4dab9fda78c00a5ab) | Express | `/students` | `/students` | 5 / 6 / 6 | 409 | PASS |
| [Omowunmiii/student-crud-api](https://github.com/Omowunmiii/student-crud-api/tree/efd670191c149a1483b3c9bec59a031ad7927433) | Express | `/students` | `/students` | 2 / 3 / 4 | 201 | VIOLATION |
| [Oy3na/simple_user_api](https://github.com/Oy3na/simple_user_api/tree/35f26afb8cd6ede871e472d96411b48072c192ac) | Flask | `/users` | `/users` | 0 / 1 / 2 | 201 | VIOLATION |
| [rabiamuhammadsaleem/crud-rest-api](https://github.com/rabiamuhammadsaleem/crud-rest-api/tree/b3d477e0d73d513d5c32499e82ee4543d4d474a2) | Express | `/items` | `/items` | 0 / 1 / 2 | 201 | VIOLATION |
| [Raghul-M/CRUD-Rest_api](https://github.com/Raghul-M/CRUD-Rest_api/tree/7e7017cb1595b9b0269570a273d436873fbbcc84) | Flask | `/user` | `/users` | 1 / 2 / 2 | 500 | PASS |
| [SamuelDouradoDev/node-api-crud-in-memory](https://github.com/SamuelDouradoDev/node-api-crud-in-memory/tree/4fdc0ba8d0d8226c3271dc2553d304659e938a06) | Express | `/users` | `/users` | 2 / 3 / 4 | 200 | VIOLATION |
| [webdevsuman/in-memory-crud-api](https://github.com/webdevsuman/in-memory-crud-api/tree/b229767bc3c516f06a7a3540d37396326c17b485) | Express | `/api/products` | `/api/products` | 0 / 1 / 2 | 201 | VIOLATION |

The batch produced 21 count `VIOLATION` results and six count `PASS` results. Five of those six `PASS` runs returned an HTTP error on retry. The remaining one, `mansiTT/express-crud-api`, returned 201 while its in-memory cache held one entry for the same email key. A count `PASS` is only a match for the state check we declared.

## How these runs were set up

The Flask services ran under Python 3.12.6, the Express services under Node 20.17.0, and the Spring services under Java 25.0.4.1. I installed dependencies for each local checkout, started the service on loopback, and kept the data in that checkout or in memory. Some clones contain seeded data, so their starting counts are above zero. The Spring JDBC sample had no table setup; I supplied [this schema](experiments/java02-schema.sql) through Spring's SQL initializer so its documented tutorial endpoint could write. The `matiaswisner/orders-api` resource file needed an ISO-8859-1 build-encoding override. These steps changed test setup, not application source.

Two Express demos listen on a fixed port already used on this machine. I preloaded a small [port shim](experiments/port-shim.cjs) to change only their listen port. Their route handlers and request bodies stayed as written. For arrays, the read-only [count adapter](experiments/array-count.py) exposed `{"count": <array length>}` to RecoveryLab; I checked the original GET response again after each run. Spring Data REST's array was nested under `/_embedded/tutorials`.

This is a compatibility exercise, not 27 bug reports. Most repos are educational CRUD examples, many do not promise safe retries, and the test covers just one write and one lost-response run per repo. It does not test all routes, concurrent calls, crash recovery, response equivalence, or external integrations. Two other Spring candidates did not build with the available Java setup and were not counted.
