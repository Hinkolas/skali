# Managed databases

Skali provisions PostgreSQL databases from the project definition. A
database is declared as a service and connected to applications through
typed output references; Skali places it on a managed pool, creates the
logical database and role, and injects the connection outputs.

```yaml
applications:
  web:
    environment:
      DATABASE_URL: "{{ databases.data.url }}"

databases:
  data:
    engine: postgres
    version: 17
```

Outputs: `host`, `port`, `name` (plain) and `username`, `password`, `url`
(secret). The application waits until the database is provisioned and
starts with the outputs injected; rotating or revealing credentials never
passes through definitions, revisions, or logs.

## Engines and versions

| Engine | Majors | Image |
| --- | --- | --- |
| `postgres` | 17, 18 | stock CloudNativePG `-system` images |

## Extensions

The stock images provide the PostgreSQL contrib set; request them per
database with `extensions: [pg_trgm, ...]`. Available today: `btree_gin`,
`btree_gist`, `citext`, `cube`, `earthdistance`, `fuzzystrmatch`,
`hstore`, `intarray`, `ltree`, `pg_stat_statements`, `pg_trgm`,
`pgcrypto`, `tablefunc`, `unaccent`, `uuid-ossp`. `pgvector` and
`postgis` are not yet supported; they arrive with a skali-built image.

## Isolation and availability

- `isolation`: `shared` (default) packs the database onto the
  installation's shared pool per engine major; `project` gives the
  environment its own pool; `dedicated` gives this database its own pool.
- `availability`: `single` (default), `asynchronous` (needs two
  database-capable nodes), `synchronous` (needs three). The shared pool's
  tier derives from the installation's database-capable node count; tier
  changes are explicit operations.

Local development (`skali dev`) runs exactly one single-instance pool:
isolation intents are honored logically but share it, higher availability
stays pending, and the pool hibernates with data retained while no running
project uses databases.

## Deleting a database

Removing a database from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown drops the
logical database and its credentials. Dedicated and project pools are
removed with their last database; the shared pool remains.
