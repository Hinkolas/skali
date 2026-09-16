package pgtune

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsableAndAutoBudget(t *testing.T) {
	// An 8 GB Hetzner node reports roughly 7.6 GiB allocatable: the 1 GiB
	// reserve wins over 10%, the shared pool takes half of the rest on the
	// 128 MiB grid.
	allocatable := int64(7782) * MiB
	usable := Usable(allocatable)
	require.Equal(t, allocatable-1*GiB, usable)
	require.Equal(t, int64(3328)*MiB, AutoBudget(ClassShared, usable))
	require.Equal(t, int64(1664)*MiB, AutoBudget(ClassEnvironment, usable))
	require.Equal(t, int64(1664)*MiB, AutoBudget(ClassDedicated, usable))

	// Big nodes reserve 10% instead of the 1 GiB floor.
	require.Equal(t, 64*GiB-64*GiB/10, Usable(64*GiB))

	// Tiny and unknown nodes floor at the minimum budget instead of zero.
	require.Equal(t, MinBudgetBytes, AutoBudget(ClassShared, 0))
	require.Equal(t, MinBudgetBytes, AutoBudget(ClassShared, Usable(1*GiB)))
	require.Equal(t, int64(0), Usable(512*MiB))

	// Quantization is stable across allocatable jitter of a few MiB.
	require.Equal(t, AutoBudget(ClassShared, Usable(allocatable)), AutoBudget(ClassShared, Usable(allocatable+7*MiB)))
}

func TestDeriveManaged(t *testing.T) {
	parameters := Derive(4*GiB, 100*GiB, true)
	require.Equal(t, map[string]string{
		"shared_buffers":           "1024MB",
		"effective_cache_size":     "3072MB",
		"maintenance_work_mem":     "256MB",
		"work_mem":                 "10MB",
		"max_connections":          "100",
		"random_page_cost":         "1.1",
		"effective_io_concurrency": "200",
		"max_wal_size":             "4096MB",
		"min_wal_size":             "1024MB",
	}, parameters)
	require.NotContains(t, parameters, "wal_keep_size", "the WAL retention cap is a dev-only measure")

	// A 32 GiB budget caps maintenance_work_mem at 2 GB.
	require.Equal(t, "2048MB", Derive(32*GiB, 500*GiB, true)["maintenance_work_mem"])
}

func TestDeriveDev(t *testing.T) {
	// The dev pool: 1 GiB budget on a 1 GiB volume. WAL sizing floors at
	// 256 MB and wal_keep_size is capped so data and WAL fit the volume.
	parameters := Derive(DevBudgetBytes, 1*GiB, false)
	require.Equal(t, "256MB", parameters["shared_buffers"])
	require.Equal(t, "768MB", parameters["effective_cache_size"])
	require.Equal(t, "64MB", parameters["maintenance_work_mem"])
	require.Equal(t, "4MB", parameters["work_mem"], "work_mem never drops below 4 MB")
	require.Equal(t, "256MB", parameters["max_wal_size"])
	require.Equal(t, "80MB", parameters["min_wal_size"])
	require.Equal(t, "64MB", parameters["wal_keep_size"])
}

func TestDeriveMinimumBudget(t *testing.T) {
	parameters := Derive(MinBudgetBytes, 10*GiB, true)
	require.Equal(t, "64MB", parameters["shared_buffers"])
	require.Equal(t, "16MB", parameters["maintenance_work_mem"])
	require.Equal(t, "4MB", parameters["work_mem"])
	require.Equal(t, "1280MB", parameters["max_wal_size"])
	require.Equal(t, "320MB", parameters["min_wal_size"])
}

func TestEffectiveOverrides(t *testing.T) {
	overrides := map[string]string{
		"work_mem":         "64MB",
		"random_page_cost": "4",
	}
	parameters := Effective(4*GiB, 100*GiB, true, overrides)
	require.Equal(t, "64MB", parameters["work_mem"], "overrides win verbatim")
	require.Equal(t, "4", parameters["random_page_cost"])
	require.Equal(t, "1024MB", parameters["shared_buffers"], "untouched keys keep their derived value")

	// max_connections re-derives work_mem: 3 GiB over 300 backends becomes
	// 3 GiB over 1200.
	parameters = Effective(4*GiB, 100*GiB, true, map[string]string{"max_connections": "400"})
	require.Equal(t, "400", parameters["max_connections"])
	require.Equal(t, "4MB", parameters["work_mem"])
	parameters = Effective(4*GiB, 100*GiB, true, map[string]string{"max_connections": "50"})
	require.Equal(t, "20MB", parameters["work_mem"])

	// Derivation is deterministic.
	require.Equal(t, Effective(4*GiB, 100*GiB, true, overrides), Effective(4*GiB, 100*GiB, true, overrides))
}

func TestValidateOverrides(t *testing.T) {
	budget := 4 * GiB
	require.NoError(t, ValidateOverrides(nil, budget))
	require.NoError(t, ValidateOverrides(map[string]string{
		"shared_buffers":               "2GB",
		"work_mem":                     "64MB",
		"max_connections":              "200",
		"random_page_cost":             "1.5",
		"checkpoint_completion_target": "0.7",
		"wal_keep_size":                "0kB",
		"effective_cache_size":         " 3GB ",
	}, budget))

	cases := []struct {
		name      string
		overrides map[string]string
		key       string
		contains  string
	}{
		{"unknown key", map[string]string{"listen_addresses": "*"}, "listen_addresses", "not a tunable parameter"},
		{"cnpg fixed key", map[string]string{"wal_level": "logical"}, "wal_level", "not a tunable parameter"},
		{"kubernetes unit", map[string]string{"shared_buffers": "1Gi"}, "shared_buffers", "kB, MB, GB or TB"},
		{"missing unit", map[string]string{"work_mem": "4096"}, "work_mem", "kB, MB, GB or TB"},
		{"lowercase unit", map[string]string{"work_mem": "4mb"}, "work_mem", "kB, MB, GB or TB"},
		{"empty", map[string]string{"work_mem": " "}, "work_mem", "empty"},
		{"budget cap", map[string]string{"shared_buffers": "3500MB"}, "shared_buffers", "exceeds 75% of the pool budget (3072MB)"},
		{"below minimum", map[string]string{"max_connections": "5"}, "max_connections", "below the minimum 10"},
		{"above maximum", map[string]string{"checkpoint_completion_target": "1.5"}, "checkpoint_completion_target", "above the maximum 1"},
		{"integer syntax", map[string]string{"max_connections": "1e3"}, "max_connections", "expects an integer"},
		{"float syntax", map[string]string{"random_page_cost": "cheap"}, "random_page_cost", "expects a number"},
		{"memory minimum", map[string]string{"max_wal_size": "16MB"}, "max_wal_size", "below the minimum 32MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOverrides(tc.overrides, budget)
			var verr *ValidationError
			require.ErrorAs(t, err, &verr)
			require.Equal(t, tc.key, verr.Key)
			require.Contains(t, verr.Message, tc.contains)
			require.Equal(t, tc.key+": "+verr.Message, err.Error())
		})
	}

	// The first failing key in sorted order is reported, so errors are stable.
	err := ValidateOverrides(map[string]string{"work_mem": "bad", "effective_cache_size": "bad"}, budget)
	var verr *ValidationError
	require.True(t, errors.As(err, &verr))
	require.Equal(t, "effective_cache_size", verr.Key)

	// Without a known budget the fraction cap is skipped, ranges still apply.
	require.NoError(t, ValidateOverrides(map[string]string{"shared_buffers": "3500MB"}, 0))
	require.Error(t, ValidateOverrides(map[string]string{"shared_buffers": "64kB"}, 0))
}

func TestMemorySyntax(t *testing.T) {
	for raw, want := range map[string]int64{
		"512MB":  512 * MiB,
		"2GB":    2 * GiB,
		"1 TB":   1 * GiB * kiB,
		"4096kB": 4 * MiB,
	} {
		got, err := ParseMemory(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"", "MB", "1.5GB", "-1", "1Gi", "1B", "99999999999999999999MB"} {
		_, err := ParseMemory(raw)
		require.ErrorIs(t, err, ErrMemorySyntax, raw)
	}
	require.Equal(t, "1024MB", FormatMemory(1*GiB))
	require.Equal(t, "10240kB", FormatMemory(10*MiB+1))
	require.Equal(t, "0kB", FormatMemory(0))
}

func TestRestartKeys(t *testing.T) {
	require.Equal(t, []string{"max_connections", "max_worker_processes", "shared_buffers", "wal_buffers"}, RestartKeys())
	require.Equal(t, []string{"shared_buffers"}, RestartRequired("work_mem", "shared_buffers", "unknown"))
	require.Empty(t, RestartRequired())
	require.Contains(t, Keys(), "effective_cache_size")
	require.Len(t, Keys(), len(Allowed))
}
