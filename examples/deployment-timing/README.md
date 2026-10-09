# Deployment timing example

A small Go service with one PostgreSQL database, one private S3 bucket, and
one application replica. Public HTTPS terminates at skali's edge; the
container serves HTTP on port 8080. The manifest deliberately keeps skali's
default startup/readiness intervals and blue-green rollout strategy.

The release command runs a transactional, advisory-lock-protected migration
and writes an S3 readiness marker. The service starts only after the release
command completes. Readiness checks the migration and reads the marker's
metadata; liveness checks the process alone. JSON logs include timestamps,
the embedded build version, migration durations, and first successful
dependency readiness. No credentials are logged.

## Run

Copy `.env.example` to `.env`, set a hostname pointing to the installation,
and generate a random `BENCH_TOKEN`. Keep `.env` private and untracked.

```sh
skali validate --remote YOUR_REMOTE --env-file .env
skali deploy --remote YOUR_REMOTE --environment baseline --env-file .env
```

The first interactive deployment can create the project and environment.
For measurements of the very first deployment, create the empty project
and environment in Studio before running the collector below.

`GET /` returns the build version and process start time. Data endpoints
require `Authorization: Bearer <BENCH_TOKEN>`:

| Endpoint | PUT | GET |
| --- | --- | --- |
| `/records/{key}` | Store up to 64 KiB of text in PostgreSQL | Read the text |
| `/objects/{key}` | Store up to 1 MiB in S3 | Read the object |

Keys contain 1–80 ASCII letters, digits, underscores, or hyphens. These are
small functional checks, not a load test. A body should be UTF-8 for records.

Run `python3 -B smoke.py` after HTTPS is reachable to check readiness,
authentication, database and S3 round trips, missing keys, and body limits.
It reads the hostname/token from the example's simple `KEY=value` `.env`
file and prints statuses and durations without credentials. After a restart,
use `python3 -B smoke.py --read-only` to verify those same records and
objects survived, without rewriting them.

## Collect an experiment

`measure.py` requires Python 3, Ruby with its standard YAML library, SSH,
Docker, and a logged-in skali remote. Run it from this directory. It uses
the remote's existing session in memory, verifies HTTPS normally, and
records no credential values. The SSH account needs `k3s kubectl` access
and Python 3. The project and environment must already exist.

```sh
python3 -B measure.py \
  --ssh root@CONTROL_NODE \
  --remote YOUR_REMOTE \
  --environment baseline \
  --domain HOSTNAME_FROM_ENV_FILE \
  --output /absolute/path/to/new-results-directory
```

The script starts Kubernetes watches and an HTTPS probe before running
`skali deploy --yes --detach` (or the API redeploy described below). It
builds for `linux/arm64`; change the explicit platform in the script if the
test cluster requires another architecture. It captures skali status
streams, journal snapshots and step logs, projected Kubernetes resource
changes, warning/normal events, queue-depth samples, and final
application/release/daemon logs. Kubernetes
projections omit container environment variables and Secret contents.

Results contain infrastructure identifiers and logs; keep the directory
private. Kubernetes timestamps are often only second-resolution. Watch and
SSE timestamps measure when the collector receives a transition, including
transport latency. Run-tree polling has a two-second cadence; the journal's
stored timestamps provide the finer operation boundaries. Parent/child
steps overlap and should not be summed.

Useful experiments:

1. First deployment: new logical database, bucket, image, and certificate
   on the existing healthy platform. Record which infrastructure was warm.
2. Warm restart: same hostname, adding `--redeploy` to the collector.
   This uses skali's deployment API to force-redeploy the active revision
   with its stored values and image. It avoids the local build context and
   does not run the unchanged revision's migration again. `--force` instead
   runs a normal CLI deployment, including local build-context evaluation.
3. Changed image: change `version.txt`, use a fresh hostname in `.env`, and
   collect another deployment. This reruns the migration hook (the SQL
   migration is already applied), the S3 write, and certificate issuance.

Changing the hostname and image together measures a combined rollout.
Use an unchanged hostname in an additional run to isolate image rollout
from certificate issuance. Changing `.env` alone creates a new revision
without rebuilding the image.

The collector does not modify platform settings, clear shared caches, or
delete application data. Its collection deadline does not cancel a
server-side deployment. Test resources remain for inspection; remove the
test environment through the normal skali teardown when finished.

## Sample background load

`measure.py` samples the queue every 10 seconds during an experiment. To
record background load on its own, for example before and after a change,
run `sample.py`. It needs Python 3, Ruby, and a remote logged in as an
instance admin.

```sh
python3 -B sample.py \
  --remote YOUR_REMOTE \
  --output /absolute/path/to/queue-samples.jsonl
```

It reads skali's observation endpoint every 5 seconds for 100 samples
(`--interval`, `--samples`) and writes one JSON line per sample: the
freshness of each observation source and, for the kernel's and the
substrate's queues, the depth, the arrivals per reason, and histograms of
queue waits and pass durations. It also records what passes spent on
database round trips, Kubernetes requests, the client's request budget, and
the environment lock, and every request by caller, verb, and resource. The
counters run since the daemon started; subtract two samples to see an
interval. A failed request is recorded as an error line and sampling
continues, so a daemon restart shows up as a gap. The output file must not
exist yet.
