package module

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
)

type stub struct{ typ string }

func (s stub) Type() string { return s.typ }
func (s stub) Decode(compiler.ProjectDefinition, string) (Service, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	require.NoError(t, r.Register(stub{"application"}))
	require.NoError(t, r.Register(stub{"database"}))
	require.Error(t, r.Register(stub{"application"}), "duplicate type must be rejected")

	m, ok := r.Get("application")
	require.True(t, ok)
	require.Equal(t, "application", m.Type())
	_, ok = r.Get("bucket")
	require.False(t, ok)
	require.Equal(t, []string{"application", "database"}, r.Types())
}

func TestOrderBatches(t *testing.T) {
	t.Parallel()
	services := []string{
		"applications.api", "applications.worker",
		"databases.main", "buckets.uploads",
	}
	dependencies := map[string][]string{
		"applications.api":    {"databases.main", "buckets.uploads"},
		"applications.worker": {"databases.main"},
	}
	batches, err := Order(services, dependencies)
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"buckets.uploads", "databases.main"},
		{"applications.api", "applications.worker"},
	}, batches)
}

func TestOrderRejectsCycle(t *testing.T) {
	t.Parallel()
	_, err := Order([]string{"a", "b"}, map[string][]string{
		"a": {"b"},
		"b": {"a"},
	})
	require.ErrorIs(t, err, ErrCycle)
}

func TestOrderRejectsUnknownReference(t *testing.T) {
	t.Parallel()
	_, err := Order([]string{"applications.api"}, map[string][]string{
		"applications.api": {"databases.missing"},
	})
	require.ErrorIs(t, err, ErrUnknownReference)
}
