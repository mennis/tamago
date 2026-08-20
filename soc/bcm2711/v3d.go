// BCM2711 V3D power, reset and AXI bridge control
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"errors"
	"fmt"
	"time"
)

// Bringing V3D up is not a mailbox operation on this SoC. The property
// interface has a power state tag with a documented V3D device id, but asking
// for it reports the domain on while the hub still answers 0xdeadbeef, which is
// what an AXI slave that is not there returns. Enabling the firmware's V3D
// clock on top of that changes nothing.
//
// What is missing is the reset and the two asynchronous AXI bridges between the
// block and the rest of the SoC, which live in the PM block and are the ARM's
// business. Linux does exactly this and no more, in bcm2835_asb_power_on.
//
// Note what this SoC does not do, since copying the older part's path is the
// obvious wrong turn: no inrush current ramp, no POWOK poll and no memory
// repair. Those steps belong to the earlier generation.

// PM block registers used for V3D. PMOffset-relative, like PMRSTC and friends in
// watchdog.go, and subject to the same rule: every write must carry PMPassword
// in its top byte or it is discarded in silence.
const (
	// PMGRAFX is the graphics domain's control register. Measured on a Pi 4
	// Model B Rev 1.5 with Linux holding V3D up: 0x00001040, i.e. PMEnab and
	// PMV3DRstN both set.
	PMGRAFX = PMOffset + 0x10c

	// PMV3DRstN is V3D's reset, active LOW — SETTING the bit releases the
	// block from reset. Note this is BIT(6), which the BCM2835 headers also
	// give to PM_PERIRSTN in a different register; they are not the same bit
	// twice.
	PMV3DRstN = 1 << 6

	// PMEnab is the domain enable. It is read and preserved rather than
	// written, because the firmware sets it and nothing here has established
	// what clearing it would do.
	PMEnab = 1 << 12
)

// The asynchronous AXI slave bridges.
//
// There are two ranges and V3D's bridges are in the second one, which the
// device tree names separately. Using the plain range writes to a bridge
// belonging to something else, at an address that answers.
//
// The board is the witness: under Linux the V3D bridges in one range read as
// running while the same offsets in the other read as halted. Two live
// registers, opposite states, one of them V3D's.
const (
	// RPiVidASBOffset is the range holding the V3D bridges on BCM2711.
	// Despite the name it is not the video decoder's alone.
	RPiVidASBOffset = 0xc11000

	ASBV3DSCtrl  = 0x08 // slave bridge control
	ASBV3DMCtrl  = 0x0c // master bridge control
	ASBAXIBrdgID = 0x20

	ASBReqStop = 1 << 0 // request the bridge to stop
	ASBAck     = 1 << 1 // the bridge has stopped
	ASBEmpty   = 1 << 2
	ASBFull    = 1 << 3

	// ASBBrdgID is the ASCII "BRDG" every bridge range reports at
	// ASBAXIBrdgID. Both ranges on the bench board read it, which is what
	// makes it usable as a cheap check that a base address is right before
	// anything is written through it.
	ASBBrdgID = 0x62726467
)

// asbSpinLimit bounds the wait for a bridge to acknowledge. Linux allows five
// microseconds; this is a spin count rather than a duration because it runs
// before any assumption about the timer is worth making, and because an
// unbounded spin on a board with nobody in the room is indistinguishable from a
// crash.
const asbSpinLimit = 1 << 20

// ErrASBTimeout is returned when an AXI bridge does not acknowledge.
var ErrASBTimeout = errors.New("bcm2711: AXI slave bridge did not acknowledge")

// ErrNoASB is returned when the ASB range does not identify itself, which means
// the base address is wrong and nothing should be written through it.
var ErrNoASB = errors.New("bcm2711: ASB range did not report BRDG")

// EnableV3D releases the V3D block from reset and opens its AXI bridges, so that
// its registers answer.
//
// It is not optional: without it every read of the V3D register region returns
// 0xdeadbeef, a bus error dressed up as data, which a driver reading an identity
// register decodes as a plausible version and core count.
//
// The clock is cycled around the reset deassert deliberately, following Linux:
// the reset needs edges to propagate, so clock on, wait, clock off, release
// reset, clock on is the sequence rather than an elaboration of one. Collapsing
// it has not been tried and should not be assumed equivalent.
//
// The V3D clock is left ENABLED on return, because the block's registers do not
// answer without it.
func EnableV3D() error {
	asb := PeripheralAddress(RPiVidASBOffset)

	// Check the range identifies itself before writing to it. A wrong base
	// here is not a fault, it is a write into some other peripheral's
	// registers with the PM password attached.
	if id := Read32(asb + ASBAXIBrdgID); id != ASBBrdgID {
		return fmt.Errorf("%w: %#x reported %#08x, want %#08x",
			ErrNoASB, asb, id, uint32(ASBBrdgID))
	}

	// Clock on, let the reset propagate, clock off.
	if err := SetClockState(CLOCK_ID_V3D, true); err != nil {
		return fmt.Errorf("bcm2711: enabling the V3D clock: %w", err)
	}

	time.Sleep(time.Microsecond)

	if err := SetClockState(CLOCK_ID_V3D, false); err != nil {
		return fmt.Errorf("bcm2711: dropping the V3D clock: %w", err)
	}

	// Deassert V3D's reset. Read-modify-write with the password, preserving
	// whatever else the firmware left in the register.
	grafx := PeripheralAddress(PMGRAFX)
	Write32(grafx, PMPassword|(Read32(grafx)|PMV3DRstN))

	if err := SetClockState(CLOCK_ID_V3D, true); err != nil {
		return fmt.Errorf("bcm2711: re-enabling the V3D clock: %w", err)
	}

	// Master bridge first, then slave. The order is Linux's and the failure
	// path unwinds in reverse, so a half-open pair is not left behind.
	if err := asbEnable(asb, ASBV3DMCtrl); err != nil {
		return fmt.Errorf("bcm2711: V3D ASB master: %w", err)
	}

	if err := asbEnable(asb, ASBV3DSCtrl); err != nil {
		_ = asbDisable(asb, ASBV3DMCtrl)
		return fmt.Errorf("bcm2711: V3D ASB slave: %w", err)
	}

	return nil
}

// DisableV3D closes the AXI bridges and puts the block back into reset, in the
// reverse order EnableV3D opened them.
//
// It does not drop the V3D clock. Whether anything else on this SoC depends on
// that clock has not been established here, and turning off a clock on the
// strength of not having looked is how a working board stops working.
func DisableV3D() error {
	asb := PeripheralAddress(RPiVidASBOffset)

	if err := asbDisable(asb, ASBV3DSCtrl); err != nil {
		return fmt.Errorf("bcm2711: V3D ASB slave: %w", err)
	}

	if err := asbDisable(asb, ASBV3DMCtrl); err != nil {
		_ = asbEnable(asb, ASBV3DSCtrl)
		return fmt.Errorf("bcm2711: V3D ASB master: %w", err)
	}

	grafx := PeripheralAddress(PMGRAFX)
	Write32(grafx, PMPassword|(Read32(grafx)&^PMV3DRstN))

	return nil
}

// V3DEnabled reports whether the block is out of reset and both of its bridges
// are running, which is the state its registers answer in.
//
// This is worth asking before concluding that a bring-up implementation is
// needed at all: a warm reboot out of Linux can leave the block up, and an image
// that inherits that state looks like it powered the GPU itself.
func V3DEnabled() (reset, master, slave bool) {
	asb := PeripheralAddress(RPiVidASBOffset)

	reset = Read32(PeripheralAddress(PMGRAFX))&PMV3DRstN != 0
	master = Read32(asb+ASBV3DMCtrl)&ASBReqStop == 0
	slave = Read32(asb+ASBV3DSCtrl)&ASBReqStop == 0

	return
}

// V3DPowerRegisters returns PM_GRAFX and the two V3D bridge control words, for
// logging a bring-up that did not take.
func V3DPowerRegisters() (grafx, master, slave uint32) {
	asb := PeripheralAddress(RPiVidASBOffset)

	return Read32(PeripheralAddress(PMGRAFX)),
		Read32(asb + ASBV3DMCtrl),
		Read32(asb + ASBV3DSCtrl)
}

// asbEnable starts a bridge: clear the stop request, then wait for the
// acknowledgement to go away.
//
// The poll is for ACK to CLEAR, which reads backwards until you notice that ACK
// means "stopped", not "done". A loop waiting for it to set on the way up never
// finishes.
func asbEnable(base uint64, reg uint64) error {
	return asbControl(base, reg, true)
}

func asbDisable(base uint64, reg uint64) error {
	return asbControl(base, reg, false)
}

func asbControl(base, reg uint64, enable bool) error {
	addr := base + reg
	val := Read32(addr)

	if enable {
		val &^= ASBReqStop
	} else {
		val |= ASBReqStop
	}

	// The password applies to these writes too, not only to the PM block
	// proper. Omit it and the bridge does not move, with no error anywhere.
	Write32(addr, PMPassword|val)

	for spins := 0; ; spins++ {
		if acked := Read32(addr)&ASBAck != 0; acked != enable {
			return nil
		}
		if spins > asbSpinLimit {
			return fmt.Errorf("%w: %#x = %#08x", ErrASBTimeout, addr, Read32(addr))
		}
	}
}
