package cnpg

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ProbeRole is the login role of a pool's connection check. It is an
// ordinary role, no superuser, so it competes for the same connection
// slots as every application and is refused exactly when they are. It
// inherits pg_monitor to count every session, which sees counts, not data,
// and holds no other privilege.
const ProbeRole = "skali_probe"

// probeIdentMap admits the instance's own OS user as the probe role over
// the local socket, under the "local" map CNPG's pg_hba applies to every
// local connection. The check needs no password, so none lives in a Secret
// or in an exec request; the socket is reachable only from inside the
// instance pod, whose processes already log in as the superuser.
const probeIdentMap = "local postgres " + ProbeRole

// ProbeRoleSpec is the managed-role entry the pool renders for the check.
func ProbeRoleSpec() Role {
	return Role{Name: ProbeRole, Login: true, InRoles: []string{"pg_monitor"}}
}

// probeQuery reads the pool's connection usage over the probe's own login:
// client connections in use (superusers included, they take the same
// slots), the server's limit, and the slots reserved for superusers and
// pg_use_reserved_connections members that ordinary roles never reach.
const probeQuery = `SELECT json_build_object(
  'max', current_setting('max_connections')::int,
  'reserved', current_setting('superuser_reserved_connections')::int
    + COALESCE(NULLIF(current_setting('reserved_connections', true), '')::int, 0),
  'used', (SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE backend_type = 'client backend'),
  'databases', (SELECT COALESCE(json_object_agg(datname, n), '{}') FROM (
    SELECT datname, count(*) AS n FROM pg_catalog.pg_stat_activity
    WHERE backend_type = 'client backend' AND datname IS NOT NULL GROUP BY datname) AS per_database))`

// ProbeCommand is the argv of one connection check in a pool instance: a
// fresh login as the probe role over the local socket, one query, and
// out. No shell; the connection lives for the length of the query.
func ProbeCommand() []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-X", "-q", "-t", "-A",
		"-U", ProbeRole, "-d", "postgres", "-c", probeQuery}
}

// ProbeUsage is one successful check's reading of the pool.
type ProbeUsage struct {
	Max       int32            `json:"max"`
	Reserved  int32            `json:"reserved"`
	Used      int32            `json:"used"`
	Databases map[string]int32 `json:"databases"`
}

// Limit is how many client connections may exist before the server refuses
// ordinary roles.
func (u ProbeUsage) Limit() int32 {
	return max(u.Max-u.Reserved, 0)
}

// ParseProbe reads the check's single JSON line.
func ParseProbe(out string) (ProbeUsage, error) {
	var usage ProbeUsage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &usage); err != nil {
		return ProbeUsage{}, fmt.Errorf("parse connection check: %w", err)
	}
	if usage.Max <= 0 {
		return ProbeUsage{}, errors.New("parse connection check: no max_connections in the answer")
	}
	return usage, nil
}

// slotRefusals are the server's answers to a login that found no free
// connection slot (SQLSTATE 53300): the pool-wide limit, the slots kept for
// superusers or reserved roles, and a per-role or per-database limit.
// psql reports a failed connection by message only, so the check matches
// the server's wording; the pools run with English messages.
var slotRefusals = []string{
	"sorry, too many clients already",
	"remaining connection slots are reserved",
	"too many connections for role",
	"too many connections for database",
}

// ProbeRefusal returns the server's refusal line when a failed check was
// turned away for lack of connection slots, and "" for any other failure:
// an exec that never reached the server, a timeout, an error that proves
// nothing about the pool's capacity.
func ProbeRefusal(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, refusal := range slotRefusals {
		index := strings.Index(text, refusal)
		if index < 0 {
			continue
		}
		line := text[index:]
		if end := strings.IndexAny(line, "\r\n"); end >= 0 {
			line = line[:end]
		}
		return strings.TrimRight(strings.TrimSpace(line), ")")
	}
	return ""
}
