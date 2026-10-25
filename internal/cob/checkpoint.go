package cob

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
)

// CheckpointContext carries the admitted program key into this lower owner.
// Content resolution and unit/VM alias validation belong to the caller
// (DESIGN_MULTIPLAYER §16.3.6); COB imports neither content nor units.
type CheckpointContext struct {
	Program          checkpoint.Definition
	ownerVM          *VM
	ownerAllocation  checkpoint.Allocation
	bindingAuthority *checkpoint.BindingAuthority
	runtimeSources   *checkpointRuntimeSources
	presentationSink *checkpointPresentationMatch
	explosionSink    *checkpointExplosionMatch
}

const (
	CheckpointContinuationNone uint8 = iota
	CheckpointContinuationSlotAim
)

// CheckpointContinuation describes the operands of one pending gameplay return.
// The bridge supplies the thread identity at installation; the combat producer
// supplies the same target and deletion key its existing receiver captures.
// These values observe the callback without executing or replacing it
// (DESIGN_MULTIPLAYER §16.3.5–§16.3.6).
type CheckpointContinuation struct {
	Kind           uint8
	ThreadSlot     uint8
	ThreadIdentity uint64
	RawUnitKey     uint32
	Target         checkpoint.Allocation
	WeaponSlot     uint8
	Mode           CallbackMode
}

type checkpointReturn struct {
	recognized   bool
	continuation CheckpointContinuation
}

// CheckpointContinuations returns a detached description of each outstanding
// gameplay receiver. Trace-only wrappers have the same zero descriptor as no
// receiver. An arbitrary receiver installed through the ordinary APIs is
// unsupported, rather than guessed from its function identity.
func (v *VM) CheckpointContinuations() ([8]CheckpointContinuation, error) {
	var out [8]CheckpointContinuation
	if v == nil {
		return out, fmt.Errorf("nil VM")
	}
	for i, fn := range v.onReturn {
		metadata := v.checkpointReturns[i]
		if fn == nil {
			if metadata != (checkpointReturn{}) {
				return out, fmt.Errorf("onReturn[%d]: metadata without receiver", i)
			}
			continue
		}
		if !metadata.recognized {
			return out, fmt.Errorf("onReturn[%d]: unrecognized gameplay receiver", i)
		}
		c := metadata.continuation
		if c.Kind == CheckpointContinuationNone {
			if c != (CheckpointContinuation{}) {
				return out, fmt.Errorf("onReturn[%d]: nonzero trace-only descriptor", i)
			}
			continue
		}
		if c.Kind != CheckpointContinuationSlotAim || c.ThreadSlot != uint8(i) ||
			!v.ThreadAliveAs(i, c.ThreadIdentity) || c.Mode != ModeDeferred ||
			c.WeaponSlot > uint8(WeaponTertiary) || c.Target.Handle == 0 || c.Target.Serial == 0 {
			return out, fmt.Errorf("onReturn[%d]: invalid slot-aim descriptor", i)
		}
		out[i] = c
	}
	return out, nil
}

// WriteCheckpoint retains these fields in lexical order: Pieces, Threads,
// activeThreadCount, anims, bindings, dirty, lastReturnIdentity,
// lastReturnValid, lastReturnValue, nextIdentity, onReturn, pieceFlags, prog,
// statics, threadIdentity, tickDenom. bindings is the composition record;
// buildRate, the Builder speed factor, is composition too: the binding sets it
// from the unit definition's recorded factor, which the content digest covers,
// so it adds no bytes here. onReturn is its logical continuation, never the
// Go function. pieceFlags
// uses source tag 1 for the unbound local store, or source 0 for an attested
// external unit store (DESIGN_MULTIPLAYER §16.3.5–§16.3.6, §16.3.55).
//
// Installation receipts and exact runtime aliases admit the reviewed production
// bindings. The parent verifies the complete unit/session graph; nonnil
// callbacks alone are not proof (DESIGN_MULTIPLAYER §16.3.55).
func (v *VM) WriteCheckpoint(e *checkpoint.Encoder, c *CheckpointContext) error {
	e.Field("VM")
	if v == nil || c == nil {
		e.Fail(fmt.Errorf("nil VM or checkpoint context"))
		return e.Err()
	}
	if v.prog == nil || c.Program.Family == 0 || c.Program.Key == "" {
		e.Field("VM.prog")
		e.Fail(fmt.Errorf("missing admitted program identity"))
		return e.Err()
	}
	if v.presentationInstructionLimit != 0 {
		e.Field("VM.presentationInstructionLimit")
		e.Fail(fmt.Errorf("presentation-only VM is unsupported"))
		return e.Err()
	}
	portBindings, portFuncs, err := v.checkpointBindings(e, c)
	if err != nil {
		return err
	}
	continuations, err := v.CheckpointContinuations()
	if err != nil {
		e.Field("VM.onReturn")
		e.Fail(err)
		return e.Err()
	}
	e.Field("VM.Pieces")
	e.Count(len(v.Pieces))
	for i := range v.Pieces {
		if err := v.Pieces[i].WriteCheckpoint(e); err != nil {
			return err
		}
	}
	for i := range v.Threads {
		v.Threads[i].writeCheckpoint(e, i)
	}
	e.Field("VM.activeThreadCount")
	e.U32(v.activeThreadCount)
	e.Field("VM.anims")
	e.Count(len(v.anims))
	for i := range v.anims {
		for axis := range v.anims[i].axes {
			a := &v.anims[i].axes[axis]
			e.Field(fmt.Sprintf("VM.anims[%d].axes[%d]", i, axis))
			// moveBusy, moveSpeed, moveTarget, spinAccel, spinActive,
			// spinTarget, turnBusy, turnSpeed, turnTarget.
			e.Bool(a.moveBusy)
			e.I32(a.moveSpeed)
			e.I32(a.moveTarget)
			e.I32(a.spinAccel)
			e.Bool(a.spinActive)
			e.I32(a.spinTarget)
			e.Bool(a.turnBusy)
			e.I32(a.turnSpeed)
			e.U16(a.turnTarget)
		}
	}
	e.Field("VM.bindings")
	// Port rows preserve explicit arm presence and legacy fallbacks (§16.3.47).
	// Remaining slots use only validated presence; render adds its storage kind.
	present := v.checkpointBindingPresence()
	for i, name := range checkpointBindingNames {
		e.Field("VM.bindings." + name)
		switch name {
		case "portBindings":
			e.Bool(len(portBindings) != 0)
			if len(portBindings) != 0 {
				e.Count(len(portBindings))
				for _, port := range portBindings {
					binding := v.portBindings[port]
					e.I64(int64(port))
					e.Bool(binding.Read != nil)
					e.Bool(binding.Write != nil)
				}
			}
		case "portFuncs":
			e.Bool(len(portFuncs) != 0)
			if len(portFuncs) != 0 {
				e.Count(len(portFuncs))
				for _, port := range portFuncs {
					e.I64(int64(port))
				}
			}
		case "renderFlags":
			e.Bool(present[i])
			if present[i] {
				e.U8(v.checkpointBindingsProof[checkpointVMRenderFlags].renderKind)
			}
		default:
			e.Bool(present[i])
		}
	}
	e.Field("VM.dirty")
	e.Bool(v.dirty)
	e.Field("VM.lastReturnIdentity")
	for _, value := range v.lastReturnIdentity {
		e.U64(value)
	}
	e.Field("VM.lastReturnValid")
	for _, value := range v.lastReturnValid {
		e.Bool(value)
	}
	e.Field("VM.lastReturnValue")
	for _, value := range v.lastReturnValue {
		e.I32(value)
	}
	e.Field("VM.nextIdentity")
	e.U64(v.nextIdentity)
	e.Field("VM.onReturn")
	for _, continuation := range continuations {
		e.U8(continuation.Kind)
		if continuation.Kind == CheckpointContinuationSlotAim {
			// This union's explicit API order overrides field sorting.
			e.U8(continuation.ThreadSlot)
			e.U64(continuation.ThreadIdentity)
			e.U32(continuation.RawUnitKey)
			e.Allocation(continuation.Target)
			e.U8(continuation.WeaponSlot)
			e.U8(uint8(continuation.Mode))
		}
	}
	e.Field("VM.pieceFlags")
	if v.renderFlagsBound {
		e.U8(0)
	} else {
		e.U8(1)
		e.Bytes(v.pieceFlags)
	}
	e.Field("VM.prog")
	e.Bool(true)
	e.Definition(c.Program)
	e.Field("VM.statics")
	e.Count(len(v.statics))
	for _, value := range v.statics {
		e.I32(value)
	}
	e.Field("VM.threadIdentity")
	for _, value := range v.threadIdentity {
		e.U64(value)
	}
	e.Field("VM.tickDenom")
	e.I32(v.tickDenom)
	return e.Err()
}

// writeCheckpoint retains PC, SP, SignalMask, Sleep, Stack, Status, WaitAxis,
// WaitPiece, WaitThread. Every physical stack cell survives, including cells
// above SP and in inactive records [04 R-COB-01 §1].
func (t *Thread) writeCheckpoint(e *checkpoint.Encoder, i int) {
	e.Field(fmt.Sprintf("VM.Threads[%d]", i))
	e.I64(int64(t.PC))
	e.I64(int64(t.SP))
	e.I32(t.SignalMask)
	e.I32(t.Sleep)
	for _, value := range t.Stack {
		e.I32(value)
	}
	e.I64(int64(t.Status))
	e.I64(int64(t.WaitAxis))
	e.I64(int64(t.WaitPiece))
	e.I64(int64(t.WaitThread))
}

// These logical binding records use absent 0 / canonical owner 1. Callback
// functions, interface addresses and binding instrumentation are not bytes.
var checkpointBindingNames = [...]string{
	"cargoContains", "carrierIdentity", "explosionSink", "portBindings",
	"portFuncs", "renderFlags", "scriptTouched", "sfxSink", "sfxVisible",
	"simRng", "transportAttach", "transportDrop",
}

func (v *VM) checkpointBindings(e *checkpoint.Encoder, c *CheckpointContext) ([]Port, []Port, error) {
	if err := v.validateCheckpointRuntime(c); err != nil {
		e.Field("VM.bindings")
		e.Fail(err)
		return nil, nil, e.Err()
	}
	return v.checkpointPorts(e, c)
}

// WriteCheckpoint retains createInvoked. The scripts owner writes and checks
// this bridge's VM edge; lifecycle tracing is observation-only state.
func (b *CallbackBridge) WriteCheckpoint(e *checkpoint.Encoder) error {
	e.Field("CallbackBridge.createInvoked")
	if b == nil {
		e.Fail(fmt.Errorf("nil callback bridge"))
		return e.Err()
	}
	e.Bool(b.createInvoked)
	return e.Err()
}

// WriteCheckpoint retains IssueBit, Ready, readyWord, in lexical field order.
// ReadyWord is deliberately not used: its save projection can hide a stored
// word when Ready is false or synthesize one when the stored word is zero
// (DESIGN_MULTIPLAYER §16.3.5).
func (s *AimSlot) WriteCheckpoint(e *checkpoint.Encoder) error {
	e.Field("AimSlot.IssueBit")
	e.Bool(s.IssueBit)
	e.Field("AimSlot.Ready")
	e.Bool(s.Ready)
	e.Field("AimSlot.readyWord")
	e.U32(s.readyWord)
	return e.Err()
}
