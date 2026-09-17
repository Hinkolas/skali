package manifest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A backup policy must select something the manifest declares: "all" of a
// class with no members snapshots nothing, which is a mistake to name at
// validation time rather than a schedule that fails on every fire.
func TestValidateBackupIncludeMatchesDeclared(t *testing.T) {
	t.Parallel()

	valid := parseForValidation(t, `
skali: v0.1.0-rc.3
name: demo
applications:
  web:
    image: example.invalid/web:1
databases:
  main:
    engine: postgres
    version: "16"
backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      databases: all
`)
	require.Empty(t, Validate(valid))

	volumes := parseForValidation(t, `
skali: v0.1.0-rc.3
name: demo
applications:
  web:
    image: example.invalid/web:1
    volumes:
      data:
        mountPath: /data
        size: 1GB
backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      databases: all
      volumes: all
`)
	require.Empty(t, Validate(volumes))

	empty := parseForValidation(t, `
skali: v0.1.0-rc.3
name: demo
applications:
  web:
    image: example.invalid/web:1
backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      databases: all
      buckets: all
`)
	require.Contains(t, diagnosticPaths(empty), "backups.daily.include")
}
