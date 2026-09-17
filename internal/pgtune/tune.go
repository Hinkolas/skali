// Package pgtune derives PostgreSQL server parameters for a managed pool
// from one number, the pool's memory budget, and validates the per-key
// overrides an administrator may layer on top. It is pure: the substrate
// renders its output into the CNPG Cluster and the API reports the same
// values, so both always agree on what a pool runs.
//
// Values inside the parameter map use PostgreSQL's own units (kB, MB, GB,
// base 1024); Kubernetes quantities (Mi, Gi) are rejected by postgresql.conf
// and never appear here.
package pgtune

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	kiB int64 = 1 << 10
	MiB int64 = 1 << 20
	GiB int64 = 1 << 30

	// DevBudgetBytes is the fixed budget of the local development pool: a
	// laptop shares its memory with the whole platform and every app.
	DevBudgetBytes = 1 * GiB
	// MinBudgetBytes is the smallest budget a pool is ever given or may be
	// configured with; below it the derived parameters fall under
	// PostgreSQL's own minimums.
	MinBudgetBytes = 256 * MiB
	// FallbackBudgetBytes sizes a pool while no database-capable node has
	// reported its memory yet (API-only mode reporting, never rendering).
	FallbackBudgetBytes = 1 * GiB
	// budgetStep quantizes automatic budgets so allocatable jitter between
	// node reports can never move shared_buffers and restart a pool.
	budgetStep = 128 * MiB

	// DefaultMaxConnections is PostgreSQL's default and CNPG's; work_mem is
	// derived from it.
	DefaultMaxConnections = 100
)

// Pool classes as recorded on database_clusters.class.
const (
	ClassShared      = "shared"
	ClassEnvironment = "environment"
	ClassDedicated   = "dedicated"
)

// Usable is the memory of a database node available to pools: the node's
// allocatable memory minus a reserve for the operating system, kubelet, the
// CNPG operator and the platform's own database.
func Usable(allocatableBytes int64) int64 {
	reserve := max(1*GiB, allocatableBytes/10)
	return max(allocatableBytes-reserve, 0)
}

// AutoBudget is the automatic budget of a pool of the given class on a node
// with the given usable memory: the shared pool takes half, environment and
// dedicated pools a quarter each, quantized to 128 MiB steps and floored at
// MinBudgetBytes.
func AutoBudget(class string, usableBytes int64) int64 {
	share := usableBytes / 4
	if class == ClassShared {
		share = usableBytes / 2
	}
	return Quantize(share)
}

// Quantize rounds a budget down to the 128 MiB grid and floors it at
// MinBudgetBytes.
func Quantize(bytes int64) int64 {
	return max(bytes/budgetStep*budgetStep, MinBudgetBytes)
}

// Derive computes the parameter set for a budget and data volume with no
// overrides. managed=false adds the local-platform WAL retention cap: the
// dev pool keeps data and WAL on a 1 GiB volume that CNPG's default
// wal_keep_size of 512 MB would fill.
func Derive(budgetBytes, storageBytes int64, managed bool) map[string]string {
	return derive(budgetBytes, storageBytes, managed, DefaultMaxConnections)
}

func derive(budgetBytes, storageBytes int64, managed bool, maxConnections int64) map[string]string {
	// Every derived quantity is floored to whole megabytes: the values are
	// what an operator reads back, and "10MB" beats "10485kB".
	sharedBuffers := wholeMiB(budgetBytes / 4)
	workMem := wholeMiB(max(4*MiB, (budgetBytes-sharedBuffers)/(maxConnections*3)))
	maxWAL := wholeMiB(clampInt64(storageBytes/8, 256*MiB, 4*GiB))
	parameters := map[string]string{
		"shared_buffers":           FormatMemory(sharedBuffers),
		"effective_cache_size":     FormatMemory(wholeMiB(budgetBytes * 3 / 4)),
		"maintenance_work_mem":     FormatMemory(wholeMiB(min(budgetBytes/16, 2*GiB))),
		"work_mem":                 FormatMemory(workMem),
		"max_connections":          strconv.FormatInt(maxConnections, 10),
		"random_page_cost":         "1.1",
		"effective_io_concurrency": "200",
		"max_wal_size":             FormatMemory(maxWAL),
		"min_wal_size":             FormatMemory(max(maxWAL/4, 80*MiB)),
	}
	if !managed {
		parameters["wal_keep_size"] = "64MB"
	}
	return parameters
}

// Effective is Derive with the overrides layered on top. An overridden
// max_connections re-derives work_mem so the per-backend budget still adds
// up; every other override replaces the derived value verbatim. Overrides
// are assumed valid (see ValidateOverrides).
func Effective(budgetBytes, storageBytes int64, managed bool, overrides map[string]string) map[string]string {
	maxConnections := int64(DefaultMaxConnections)
	if raw, ok := overrides["max_connections"]; ok {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && parsed > 0 {
			maxConnections = parsed
		}
	}
	parameters := derive(budgetBytes, storageBytes, managed, maxConnections)
	for key, value := range overrides {
		parameters[key] = value
	}
	return parameters
}

// Type is the value syntax of one parameter.
type Type int

const (
	// TypeMemory is a PostgreSQL memory quantity: an integer with a kB, MB,
	// GB or TB unit.
	TypeMemory Type = iota
	// TypeInt is a plain integer.
	TypeInt
	// TypeFloat is a decimal number.
	TypeFloat
)

// Kind describes one overridable parameter: its syntax, range, whether a
// change restarts the instances, and for memory keys an optional cap as a
// fraction of the pool budget.
type Kind struct {
	Type Type
	// Min and Max bound the value: bytes for memory keys, the number itself
	// otherwise. Max 0 means unbounded.
	Min, Max float64
	// Restart reports that PostgreSQL only picks the change up on restart;
	// CNPG restarts the instances (replicas first, then the primary).
	Restart bool
	// MaxBudgetFraction caps a memory key at this fraction of the budget
	// (0 = uncapped).
	MaxBudgetFraction float64
}

// Allowed is the curated set of parameters an administrator may override.
// Everything CNPG fixes itself (wal_level, hot_standby, listen_addresses,
// ssl, archive_*, port, logging, shared_preload_libraries) is deliberately
// absent, so an accepted override can never be rejected by the operator.
var Allowed = map[string]Kind{
	"shared_buffers":                  {Type: TypeMemory, Min: float64(128 * kiB), Restart: true, MaxBudgetFraction: 0.75},
	"effective_cache_size":            {Type: TypeMemory, Min: float64(8 * kiB)},
	"maintenance_work_mem":            {Type: TypeMemory, Min: float64(1 * MiB), MaxBudgetFraction: 1},
	"work_mem":                        {Type: TypeMemory, Min: float64(64 * kiB), MaxBudgetFraction: 1},
	"max_connections":                 {Type: TypeInt, Min: 10, Max: 10000, Restart: true},
	"random_page_cost":                {Type: TypeFloat, Min: 0, Max: 1000},
	"effective_io_concurrency":        {Type: TypeInt, Min: 0, Max: 1000},
	"max_wal_size":                    {Type: TypeMemory, Min: float64(32 * MiB)},
	"min_wal_size":                    {Type: TypeMemory, Min: float64(32 * MiB)},
	"wal_keep_size":                   {Type: TypeMemory, Min: 0},
	"wal_buffers":                     {Type: TypeMemory, Min: float64(32 * kiB), Max: float64(2047 * MiB), Restart: true},
	"checkpoint_completion_target":    {Type: TypeFloat, Min: 0, Max: 1},
	"default_statistics_target":       {Type: TypeInt, Min: 1, Max: 10000},
	"max_parallel_workers_per_gather": {Type: TypeInt, Min: 0, Max: 1024},
	"max_parallel_workers":            {Type: TypeInt, Min: 0, Max: 1024},
	"max_worker_processes":            {Type: TypeInt, Min: 0, Max: 262143, Restart: true},
}

// Keys lists the overridable parameters, sorted.
func Keys() []string {
	keys := make([]string, 0, len(Allowed))
	for key := range Allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// RestartKeys lists the overridable parameters whose change restarts the
// instances, sorted.
func RestartKeys() []string {
	var keys []string
	for key, kind := range Allowed {
		if kind.Restart {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// RestartRequired filters the given keys down to those that restart the
// instances, sorted.
func RestartRequired(keys ...string) []string {
	var out []string
	for _, key := range keys {
		if Allowed[key].Restart {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// ValidationError names the override that failed and why.
type ValidationError struct {
	Key     string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Key == "" {
		return e.Message
	}
	return e.Key + ": " + e.Message
}

// ValidateOverrides checks every override against Allowed: known key,
// value syntax for its type, range, and for memory keys the budget cap when
// budgetBytes is known (> 0). The first failure is returned as a
// *ValidationError, keys checked in sorted order so errors are stable.
func ValidateOverrides(overrides map[string]string, budgetBytes int64) error {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		kind, ok := Allowed[key]
		if !ok {
			return &ValidationError{Key: key, Message: "not a tunable parameter; allowed: " + strings.Join(Keys(), ", ")}
		}
		raw := strings.TrimSpace(overrides[key])
		if raw == "" {
			return &ValidationError{Key: key, Message: "value is empty"}
		}
		var value float64
		switch kind.Type {
		case TypeMemory:
			bytes, err := ParseMemory(raw)
			if err != nil {
				return &ValidationError{Key: key, Message: err.Error()}
			}
			value = float64(bytes)
			if kind.MaxBudgetFraction > 0 && budgetBytes > 0 && value > kind.MaxBudgetFraction*float64(budgetBytes) {
				return &ValidationError{Key: key, Message: fmt.Sprintf("exceeds %d%% of the pool budget (%s)",
					int(kind.MaxBudgetFraction*100), FormatMemory(int64(kind.MaxBudgetFraction*float64(budgetBytes))))}
			}
		case TypeInt:
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return &ValidationError{Key: key, Message: "expects an integer"}
			}
			value = float64(parsed)
		case TypeFloat:
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return &ValidationError{Key: key, Message: "expects a number"}
			}
			value = parsed
		}
		if value < kind.Min {
			return &ValidationError{Key: key, Message: "below the minimum " + formatBound(kind, kind.Min)}
		}
		if kind.Max > 0 && value > kind.Max {
			return &ValidationError{Key: key, Message: "above the maximum " + formatBound(kind, kind.Max)}
		}
	}
	return nil
}

func formatBound(kind Kind, bound float64) string {
	if kind.Type == TypeMemory {
		return FormatMemory(int64(bound))
	}
	return strconv.FormatFloat(bound, 'f', -1, 64)
}

// ErrMemorySyntax describes the accepted memory syntax.
var ErrMemorySyntax = errors.New("expects an integer with a kB, MB, GB or TB unit (PostgreSQL syntax, e.g. 512MB)")

var memoryPattern = regexp.MustCompile(`^(\d+)\s*(kB|MB|GB|TB)$`)

var memoryUnits = map[string]int64{"kB": kiB, "MB": MiB, "GB": GiB, "TB": GiB * kiB}

// ParseMemory parses a PostgreSQL memory quantity into bytes. The unit is
// mandatory: a bare number means "in the parameter's own unit" to
// PostgreSQL (kB for some keys, 8 kB blocks for others), which is exactly
// the ambiguity this package refuses to store.
func ParseMemory(raw string) (int64, error) {
	match := memoryPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return 0, ErrMemorySyntax
	}
	number, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, ErrMemorySyntax
	}
	unit := memoryUnits[match[2]]
	if number > (1<<62)/unit {
		return 0, ErrMemorySyntax
	}
	return number * unit, nil
}

// FormatMemory renders bytes in PostgreSQL syntax: whole megabytes when
// the value divides evenly (the common case), kilobytes otherwise.
func FormatMemory(bytes int64) string {
	if bytes <= 0 {
		return "0kB"
	}
	if bytes%MiB == 0 {
		return strconv.FormatInt(bytes/MiB, 10) + "MB"
	}
	return strconv.FormatInt(bytes/kiB, 10) + "kB"
}

func clampInt64(value, low, high int64) int64 {
	return min(max(value, low), high)
}

// wholeMiB floors bytes to a whole number of megabytes, never below one.
func wholeMiB(bytes int64) int64 {
	return max(bytes/MiB*MiB, MiB)
}

// Budget is one pool's resolved memory budget.
type Budget struct {
	Bytes int64
	// Auto reports the budget was derived rather than set explicitly.
	Auto bool
	// Node names the database-capable node an automatic budget was sized
	// from; empty for explicit budgets and on the local platform.
	Node string
	// Capped reports an automatic budget was reduced below its class share
	// so the live pools together fit the node.
	Capped bool
}
