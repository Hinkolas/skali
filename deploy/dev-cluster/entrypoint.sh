#!/bin/sh
# Entrypoint for skali dev-cluster nodes. Starts the node's own Docker engine
# (DinD), then runs skalid in the configured ROLE:
#   master — migrate, ensure the dev admin user, serve
#   worker — self-provision a join token via the master's REST API, enroll,
#            run the agent (enrollment is skipped when an identity already
#            exists, so plain container restarts don't create duplicate nodes)
set -eu

# --- inner Docker engine ----------------------------------------------------
# The stock DinD bootstrap; skalid (and later Traefik/app containers) talk to
# it over the local socket. TLS is disabled via DOCKER_TLS_CERTDIR="" in the
# compose environment.
dockerd-entrypoint.sh dockerd >/var/log/dockerd.log 2>&1 &
i=0
until docker info >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "inner dockerd did not come up:" >&2
    tail -20 /var/log/dockerd.log >&2
    exit 1
  fi
  sleep 1
done
echo "inner dockerd ready"

case "${ROLE:?ROLE must be master or worker}" in
master)
  skalid migrate up
  # Idempotent-ish bootstrap: creation fails when the user already exists.
  printf '%s' "${ADMIN_PASSWORD}" | skalid user create \
    --email "${ADMIN_EMAIL}" --name "Dev Admin" --role admin --password-stdin ||
    echo "admin user already exists"
  exec skalid serve
  ;;

worker)
  MASTER_API="http://master:7070"
  i=0
  until curl -fsS "${MASTER_API}/healthz" >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -gt 120 ]; then
      echo "master API not reachable at ${MASTER_API}" >&2
      exit 1
    fi
    sleep 1
  done

  if [ ! -f /var/lib/skalid/node.json ]; then
    # A fresh login is sudo-fresh, so minting the join token works right away.
    SESSION=$(curl -fsS -X POST "${MASTER_API}/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASSWORD}\"}" |
      jq -r .session.token)
    ROLES_JSON=$(printf '%s' "${NODE_ROLES:-worker}" | jq -Rc 'split(",")')
    JOIN_TOKEN=$(curl -fsS -X POST "${MASTER_API}/v1/nodes/tokens" \
      -H "Authorization: Bearer ${SESSION}" -H 'Content-Type: application/json' \
      -d "{\"roles\":${ROLES_JSON}}" | jq -r .token)
    curl -fsS -X POST "${MASTER_API}/v1/auth/logout" \
      -H "Authorization: Bearer ${SESSION}" >/dev/null || true

    skalid enroll --master master:7443 --token "${JOIN_TOKEN}" \
      --advertise-addr "$(hostname):7443"
  else
    echo "identity exists, skipping enrollment"
  fi
  exec skalid agent
  ;;

*)
  echo "unknown ROLE=${ROLE} (expected master or worker)" >&2
  exit 1
  ;;
esac
