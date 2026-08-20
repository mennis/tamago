// Broadcom PM-block watchdog register logic
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package pmwdt implements the register layout and timeout arithmetic of the
// Broadcom PM block watchdog, apart from the code that touches hardware.
//
// It exists because a SoC package cannot be built off-target: it imports arm64,
// which reaches runtime.MemRegion, so no amount of care in writing MMIO-free
// helpers makes them testable where they sit. The arithmetic lives here, where
// it depends on nothing but time, and the driver keeps the register writes.
// The part that decides whether a requested timeout is representable is the
// part where being wrong is dangerous rather than merely broken.
//
// The block is the BCM2835 style watchdog carried by every SoC in this family,
// not an SP805.
package pmwdt

import "time"

// Register offsets from the PM block base.
const (
	RSTC = 0x1c // reset control
	RSTS = 0x20 // reset status: why the board last came up
	WDOG = 0x24 // countdown value
)

const (
	// Password must occupy the top byte of EVERY write to this block. Omit it
	// and the write is silently discarded — no error, the register simply does
	// not change, which is the failure most likely to be mistaken for a dead
	// watchdog rather than a missing password.
	Password = 0x5a000000

	// TimeSet masks the 20-bit down-counter field.
	TimeSet = 0x000fffff

	// WRCFGClr clears the WRCFG field; WRCFGFullReset selects a full reset as
	// the action taken when the counter reaches zero.
	WRCFGClr       = 0xffffffcf
	WRCFGFullReset = 0x00000020

	// RSTCReset returns the reset controller to its idle configuration, which
	// is how the watchdog is stopped. The value is byte-for-byte Linux's
	// bcm2835_wdt_stop.
	RSTCReset = 0x00000102
)

// Period is the counter's fixed tick. It is not derived from any configurable
// clock, which is what makes the arithmetic here independent of board clocking.
const Period = 16 * time.Microsecond

// MaxTimeout is the longest armable window: the 20-bit counter at Period.
const MaxTimeout = time.Duration(TimeSet) * Period

// TicksFor converts a timeout to a counter value. A timeout that does not fit
// is refused rather than saturated: arming a fraction of what was asked for
// resets a board in the middle of work its owner believed was protected, and
// reports success while doing it.
//
// A positive timeout shorter than one tick arms at the shortest expressible
// count rather than at zero, which the hardware reads as already expired.
func TicksFor(timeout time.Duration) (ticks uint32, ok bool) {
	if timeout <= 0 {
		return 0, false
	}

	t := uint64(timeout) / uint64(Period)
	if t == 0 {
		t = 1
	}
	if t > TimeSet {
		return 0, false
	}

	return uint32(t), true
}

// Remaining converts a raw WDOG reading back to a duration.
func Remaining(reg uint32) time.Duration {
	return time.Duration(reg&TimeSet) * Period
}

// RegMask is the part of a PM register a write can carry. The top byte is the
// password field on write, so nothing read back from bits [31:24] can be
// preserved through a read-modify-write.
const RegMask = 0x00ffffff

// ArmRSTC returns the value to write to RSTC to arm the watchdog, given the
// register's current contents.
//
// It is a read-modify-write: the WRCFG field is replaced and everything else in
// the low 24 bits is preserved, because RSTC carries reset-controller state this
// driver has no business changing. The password is added here so a caller cannot
// forget it.
//
// The mask to RegMask is load-bearing rather than tidy. Without it, any bit set
// in the top byte of the value read back ORs straight into the password field —
// and a PM write whose password is not exactly 0x5a is discarded by the hardware
// in silence. The watchdog would simply not arm, Start would return nil, and the
// only symptom would be a board that fails to reset when it hangs, which is the
// one moment nobody is watching. Found by the tests in this package.
func ArmRSTC(current uint32) uint32 {
	return Password | (current & WRCFGClr & RegMask) | WRCFGFullReset
}

// StopRSTC returns the value to write to RSTC to stop the watchdog.
func StopRSTC() uint32 {
	return Password | RSTCReset
}

// LoadWDOG returns the value to write to WDOG for a given tick count.
func LoadWDOG(ticks uint32) uint32 {
	return Password | (ticks & TimeSet)
}
