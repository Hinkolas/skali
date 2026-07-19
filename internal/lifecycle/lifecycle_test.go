package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func machine(states []string, transitions map[string][]string) Machine[string] {
	return Machine[string]{States: states, Transitions: transitions}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	m := machine([]string{"a", "b", "c"}, map[string][]string{
		"a": {"b", "c"},
		"b": {"c"},
	})
	require.NoError(t, Verify(m))
	require.Equal(t, "a", m.Initial())
	require.True(t, m.Valid("b"))
	require.False(t, m.Valid("d"))
	require.True(t, m.Terminal("c"))
	require.False(t, m.Terminal("a"))
	require.False(t, m.Terminal("d"))
	require.True(t, m.Can("a", "b"))
	require.False(t, m.Can("b", "a"))
	require.False(t, m.Can("c", "a"))
}

func TestVerifyViolations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		machine Machine[string]
		message string
	}{
		{
			name:    "no states",
			machine: machine(nil, nil),
			message: "declares no states",
		},
		{
			name:    "duplicate state",
			machine: machine([]string{"a", "a"}, map[string][]string{"a": nil}),
			message: "declared twice",
		},
		{
			name:    "undeclared source",
			machine: machine([]string{"a"}, map[string][]string{"b": {"a"}}),
			message: "not a declared state",
		},
		{
			name:    "undeclared target",
			machine: machine([]string{"a"}, map[string][]string{"a": {"b"}}),
			message: "targets an undeclared state",
		},
		{
			name:    "self transition",
			machine: machine([]string{"a", "b"}, map[string][]string{"a": {"a", "b"}}),
			message: "transitions to itself",
		},
		{
			name:    "unreachable state",
			machine: machine([]string{"a", "b", "c"}, map[string][]string{"a": {"b"}}),
			message: "unreachable from the initial state",
		},
		{
			name: "no terminal reachable",
			machine: machine([]string{"a", "b"}, map[string][]string{
				"a": {"b"},
				"b": {"a"},
			}),
			message: "cannot reach a terminal state",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := Verify(testCase.machine)
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.message)
		})
	}
}
