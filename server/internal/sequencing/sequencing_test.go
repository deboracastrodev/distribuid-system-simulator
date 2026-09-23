package sequencing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecide(t *testing.T) {
	inProgress := OrderState{Exists: true, PlanID: "p1", LastSeq: 2, Status: "inventory_validated"}

	tests := []struct {
		name  string
		state OrderState
		plan  string
		seq   int
		want  Outcome
	}{
		{"first event of a new order", OrderState{}, "p1", 1, Apply},
		{"later event of a new order waits for seq 1", OrderState{}, "p1", 3, Buffer},
		{"next consecutive event", inProgress, "p1", 3, Apply},
		{"replay of the last applied event", inProgress, "p1", 2, Duplicate},
		{"replay of an older event", inProgress, "p1", 1, Duplicate},
		{"gap is buffered, never applied", inProgress, "p1", 4, Buffer},
		{"event from another plan", inProgress, "p2", 3, PlanMismatch},
		{"zombie event after abort", OrderState{Exists: true, PlanID: "p1", LastSeq: 1, Status: "aborted"}, "p1", 2, Discard},
		{"zombie replay after abort", OrderState{Exists: true, PlanID: "p1", LastSeq: 1, Status: "aborted"}, "p1", 1, Discard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Decide(tt.state, tt.plan, tt.seq))
		})
	}
}

func TestDecideAbort(t *testing.T) {
	tests := []struct {
		name  string
		state OrderState
		plan  string
		want  Outcome
	}{
		{"order without events", OrderState{}, "p1", Tombstone},
		{"order in progress", OrderState{Exists: true, PlanID: "p1", LastSeq: 2, Status: "inventory_validated"}, "p1", Apply},
		{"order already aborted", OrderState{Exists: true, PlanID: "p1", Status: "aborted"}, "p1", Duplicate},
		{"completed order is terminal", OrderState{Exists: true, PlanID: "p1", LastSeq: 5, Status: "completed"}, "p1", Discard},
		{"abort from another plan", OrderState{Exists: true, PlanID: "p1", Status: "pending"}, "p2", PlanMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DecideAbort(tt.state, tt.plan))
		})
	}
}
