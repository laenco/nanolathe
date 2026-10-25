package ai

import (
	"fmt"
	"math"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// ApplicationOperands holds already-observed identities in emission order.
// Adapters attest allocation and admitted definition identity before calling
// these value-only codecs (DESIGN_MULTIPLAYER §16.3.23).
type ApplicationOperands struct {
	Actors  []checkpoint.Allocation
	Target  *checkpoint.Allocation
	Product *checkpoint.Definition
}

type ClassicApplicationIntent struct {
	Kind        uint8
	Code        int64
	ResolvedRow uint8
	Modifier    uint8
	Argument    int32
	X, Y, Z     numeric.Fixed
	RawTarget   uint32
	UnitKey     string
	BuildKind   int64
	Count       int64
	RequestTick uint32
	Active      bool
	Operands    ApplicationOperands
}

type ModernApplicationIntent struct {
	Kind                       uint8
	Queued                     bool
	RawTarget                  uint32
	X, Z                       int32
	ProductIndex               int32
	ProductKey                 string
	Slot, Count, Spot, Spacing int32
	Keep, Exact                bool
	RowNear                    int32
	Operands                   ApplicationOperands
}

// WriteCheckpoint writes the selected Classic variant and its operands in
// §16.3.23 order. Row zero, raw target and signed producer values are retained;
// fields belonging to another variant are excluded, never normalized.
func (v ClassicApplicationIntent) WriteCheckpoint(e *checkpoint.Encoder) error {
	const path = "ai.intent.classic"
	if e == nil {
		return aiCheckpointError(path, "a checkpoint encoder")
	}
	e.Field(path)
	if v.Kind < 1 || v.Kind > 3 {
		e.Fail(aiCheckpointError(path+".Kind", "Classic intent kind 1, 2 or 3"))
		return e.Err()
	}
	if err := validateApplicationOperands(v.Operands); err != nil {
		e.Fail(err)
		return e.Err()
	}
	e.U8(v.Kind)
	switch v.Kind {
	case 1:
		e.I64(v.Code)
		e.U8(v.ResolvedRow)
		e.U8(v.Modifier)
		e.I32(v.Argument)
		e.I64(int64(v.X))
		e.I64(int64(v.Y))
		e.I64(int64(v.Z))
		e.U32(v.RawTarget)
	case 2:
		e.I64(v.BuildKind)
		e.String(v.UnitKey)
		e.I64(int64(v.X))
		e.I64(int64(v.Z))
		e.I64(v.Count)
		e.U32(v.RequestTick)
	case 3:
		e.Bool(v.Active)
	}
	writeApplicationOperands(e, v.Operands)
	return e.Err()
}

// ModernIntentKinds is the Modern command vocabulary's size: aikit.CmdKind
// 1 through this (aikit's TestModernIntentKindsCoverTheVocabulary holds
// the two together).
const ModernIntentKinds = 21

// WriteCheckpoint retains the existing Modern command vocabulary and all
// fixed operands, including batch RowNear (DESIGN_MULTIPLAYER §16.3.23).
// This lower package deliberately does not import the controller adapter.
func (v ModernApplicationIntent) WriteCheckpoint(e *checkpoint.Encoder) error {
	const path = "ai.intent.modern"
	if e == nil {
		return aiCheckpointError(path, "a checkpoint encoder")
	}
	e.Field(path)
	if v.Kind < 1 || v.Kind > ModernIntentKinds {
		e.Fail(aiCheckpointError(path+".Kind", fmt.Sprintf("Modern command kind 1 through %d", ModernIntentKinds)))
		return e.Err()
	}
	if err := validateApplicationOperands(v.Operands); err != nil {
		e.Fail(err)
		return e.Err()
	}
	e.U8(v.Kind)
	e.Bool(v.Queued)
	e.U32(v.RawTarget)
	e.I32(v.X)
	e.I32(v.Z)
	e.I32(v.ProductIndex)
	e.String(v.ProductKey)
	e.I32(v.Slot)
	e.I32(v.Count)
	e.I32(v.Spot)
	e.I32(v.Spacing)
	e.Bool(v.Keep)
	e.Bool(v.Exact)
	e.I32(v.RowNear)
	writeApplicationOperands(e, v.Operands)
	return e.Err()
}

func validApplicationAllocation(v checkpoint.Allocation) bool {
	return v.Handle != 0 && v.Serial != 0
}

func validateApplicationOperands(v ApplicationOperands) error {
	if uint64(len(v.Actors)) > math.MaxUint32 {
		return aiCheckpointError("ai.intent.actors", "a u32 actor count")
	}
	for i, actor := range v.Actors {
		if !validApplicationAllocation(actor) {
			return aiCheckpointError(fmt.Sprintf("ai.intent.actors[%d]", i), "a nonzero allocation handle and serial")
		}
	}
	if v.Target != nil && !validApplicationAllocation(*v.Target) {
		return aiCheckpointError("ai.intent.target", "a nonzero allocation handle and serial")
	}
	if p := v.Product; p != nil && (p.Family != content.SimulationFamilyCatalog || p.Ordinal == 0 || !strings.HasPrefix(p.Key, "unit/") || len(p.Key) == len("unit/")) {
		return aiCheckpointError("ai.intent.product", "an admitted catalog unit identity with nonzero ordinal and nonempty unit/ key")
	}
	return nil
}

func writeApplicationOperands(e *checkpoint.Encoder, v ApplicationOperands) {
	e.Count(len(v.Actors))
	for _, actor := range v.Actors {
		e.Allocation(actor)
	}
	e.Bool(v.Target != nil)
	if v.Target != nil {
		e.Allocation(*v.Target)
	}
	e.Bool(v.Product != nil)
	if v.Product != nil {
		e.Definition(*v.Product)
	}
}
