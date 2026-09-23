// Package sequencing holds the ordering rules for plan events. It does no I/O,
// so the rules can be tested exhaustively; the db package applies them inside
// the transaction that persists the outcome.
package sequencing

import "github.com/user/nexus-server/pkg/models"

// Outcome is what processing an event does to its order.
type Outcome string

const (
	// Apply persists the event (or, for ABORT_PLAN, aborts the order).
	Apply Outcome = "APPLY"
	// Duplicate means the event was already applied.
	Duplicate Outcome = "DUPLICATE"
	// Buffer parks the event until its predecessor is applied.
	Buffer Outcome = "BUFFER"
	// Discard drops the event because the order reached a state that rejects it.
	Discard Outcome = "DISCARD"
	// PlanMismatch rejects an event whose plan does not own the order.
	PlanMismatch Outcome = "PLAN_MISMATCH"
	// Tombstone records an abort for an order that has no events yet, so events
	// arriving later for the same plan are discarded.
	Tombstone Outcome = "TOMBSTONE"
)

// OrderState is the persisted progress of an order. The zero value is an order
// that does not exist yet.
type OrderState struct {
	Exists  bool
	PlanID  string
	LastSeq int
	Status  string
}

// Decide returns what to do with sequenced event seq of planID.
func Decide(s OrderState, planID string, seq int) Outcome {
	if !s.Exists {
		if seq == 1 {
			return Apply
		}
		return Buffer
	}
	if s.PlanID != planID {
		return PlanMismatch
	}
	if s.Status == models.StatusAborted {
		return Discard
	}
	switch {
	case seq <= s.LastSeq:
		return Duplicate
	case seq == s.LastSeq+1:
		return Apply
	default:
		return Buffer
	}
}

// DecideAbort returns what to do with an ABORT_PLAN for planID.
func DecideAbort(s OrderState, planID string) Outcome {
	if !s.Exists {
		return Tombstone
	}
	if s.PlanID != planID {
		return PlanMismatch
	}
	switch s.Status {
	case models.StatusAborted:
		return Duplicate
	case models.StatusCompleted:
		return Discard
	default:
		return Apply
	}
}
