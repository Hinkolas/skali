// Package lifecycle provides the declarative state machines behind contract
// vocabularies such as run statuses and claim phases. A Machine is data, not
// behavior: domain packages declare their states and transitions, consumers
// guard status changes with Can and Terminal, and Verify enforces the
// structural invariants every contract machine must hold.
package lifecycle

import (
	"fmt"
	"slices"
)

// Machine declares a status vocabulary. States lists every status in display
// order and its first entry is the initial status. A status with no outgoing
// transitions is terminal.
type Machine[S comparable] struct {
	States      []S
	Transitions map[S][]S
}

func (m Machine[S]) Initial() S {
	return m.States[0]
}

func (m Machine[S]) Valid(status S) bool {
	return slices.Contains(m.States, status)
}

func (m Machine[S]) Terminal(status S) bool {
	return m.Valid(status) && len(m.Transitions[status]) == 0
}

func (m Machine[S]) Can(from, to S) bool {
	return slices.Contains(m.Transitions[from], to)
}

// Verify checks the invariants shared by all contract machines: at least one
// state, no duplicate states, transitions only between declared states, no
// self-transitions, every state reachable from the initial state, and no
// state from which a terminal state is unreachable.
func Verify[S comparable](m Machine[S]) error {
	if len(m.States) == 0 {
		return fmt.Errorf("machine declares no states")
	}
	declared := make(map[S]bool, len(m.States))
	for _, state := range m.States {
		if declared[state] {
			return fmt.Errorf("state %v is declared twice", state)
		}
		declared[state] = true
	}
	for from, targets := range m.Transitions {
		if !declared[from] {
			return fmt.Errorf("transition source %v is not a declared state", from)
		}
		for _, to := range targets {
			if !declared[to] {
				return fmt.Errorf("transition %v -> %v targets an undeclared state", from, to)
			}
			if to == from {
				return fmt.Errorf("state %v transitions to itself", from)
			}
		}
	}
	for state, reachable := range m.reachable(m.Initial()) {
		if !reachable {
			return fmt.Errorf("state %v is unreachable from the initial state", state)
		}
	}
	for _, state := range m.States {
		if !m.reachesTerminal(state) {
			return fmt.Errorf("state %v cannot reach a terminal state", state)
		}
	}
	return nil
}

// reachable returns, for every declared state, whether it can be reached from
// start through zero or more transitions.
func (m Machine[S]) reachable(start S) map[S]bool {
	visited := make(map[S]bool, len(m.States))
	queue := []S{start}
	visited[start] = true
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range m.Transitions[current] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	result := make(map[S]bool, len(m.States))
	for _, state := range m.States {
		result[state] = visited[state]
	}
	return result
}

func (m Machine[S]) reachesTerminal(state S) bool {
	for reached, ok := range m.reachable(state) {
		if ok && m.Terminal(reached) {
			return true
		}
	}
	return false
}
