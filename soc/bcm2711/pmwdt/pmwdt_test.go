// Broadcom PM-block watchdog register logic
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pmwdt

import (
	"testing"
	"time"
)

// The tick is 16µs, so a second is 62500 counts and 15 seconds is 937500 —
// inside the 20-bit field, which tops out at 0xfffff.
func TestTicksForKnownValues(t *testing.T) {
	for _, tc := range []struct {
		in    time.Duration
		ticks uint32
		ok    bool
	}{
		{15 * time.Second, 937500, true},
		{time.Second, 62500, true},
		{Period, 1, true},
		{MaxTimeout, TimeSet, true},

		// A positive timeout shorter than one tick must still arm, at the
		// shortest count. Rounding to zero would program a counter the
		// hardware reads as already expired and acts on immediately.
		{time.Microsecond, 1, true},
		{Period - 1, 1, true},

		// Non-positive cannot arm.
		{0, 0, false},
		{-time.Second, 0, false},
		{-MaxTimeout, 0, false},

		// Beyond the counter must REFUSE, never saturate. Saturating reports
		// success while arming a window an order of magnitude shorter than the
		// caller asked for, which resets a board in the middle of work its
		// owner believed was protected.
		{MaxTimeout + Period, 0, false},
		{time.Minute, 0, false},
		{time.Hour, 0, false},
	} {
		ticks, ok := TicksFor(tc.in)
		if ok != tc.ok || ticks != tc.ticks {
			t.Errorf("TicksFor(%v) = (%d, %t), want (%d, %t)",
				tc.in, ticks, ok, tc.ticks, tc.ok)
		}
	}
}

// Whatever TicksFor accepts must fit the field the hardware has, and must never
// be zero. A value wider than 20 bits would put its high bits in neighbouring
// register fields; a zero arms an already-expired counter.
func TestAcceptedTicksAreAlwaysArmable(t *testing.T) {
	for d := time.Nanosecond; d < 30*time.Second; d = d*7/5 + 1 {
		ticks, ok := TicksFor(d)
		if !ok {
			continue
		}
		if ticks == 0 {
			t.Fatalf("TicksFor(%v) accepted but returned 0 ticks", d)
		}
		if ticks > TimeSet {
			t.Fatalf("TicksFor(%v) = %d, wider than the %d-tick counter", d, ticks, TimeSet)
		}
	}
}

// The boundary is exact on both sides, since it is the one place saturation
// would be tempting.
func TestMaxTimeoutBoundaryIsExact(t *testing.T) {
	if got, want := MaxTimeout, time.Duration(TimeSet)*Period; got != want {
		t.Fatalf("MaxTimeout = %v, want %v", got, want)
	}
	if ticks, ok := TicksFor(MaxTimeout); !ok || ticks != TimeSet {
		t.Errorf("TicksFor(MaxTimeout) = (%d, %t), want (%d, true)", ticks, ok, TimeSet)
	}
	// A request between MaxTimeout and MaxTimeout+Period truncates to the
	// maximum count. That is QUANTIZATION, not the saturation the doc forbids:
	// it rounds DOWN by less than one tick, so the watchdog fires marginally
	// early rather than an order of magnitude late. A full tick past the
	// maximum must be refused.
	if ticks, ok := TicksFor(MaxTimeout + Period/2); !ok || ticks != TimeSet {
		t.Errorf("TicksFor(MaxTimeout + half a tick) = (%d, %t), want (%d, true)",
			ticks, ok, TimeSet)
	}
	if _, ok := TicksFor(MaxTimeout + Period); ok {
		t.Error("TicksFor accepted a full tick past MaxTimeout")
	}
}

// Whatever is accepted must be within one tick of what was asked for, in the
// safe direction where it differs at all.
func TestAcceptedTimeoutsAreWithinOneTick(t *testing.T) {
	for d := time.Nanosecond; d < MaxTimeout; d = d*13/8 + 1 {
		ticks, ok := TicksFor(d)
		if !ok {
			t.Fatalf("TicksFor(%v) refused a timeout inside the counter range", d)
		}
		got := Remaining(ticks)
		if diff := got - d; diff >= Period || diff <= -Period {
			t.Errorf("TicksFor(%v) armed %v — off by %v, more than one tick", d, got, diff)
		}
	}
}

// Every value written to this block must carry the password in its top byte or
// the hardware discards the write in silence.
func TestEveryWriteCarriesThePassword(t *testing.T) {
	check := func(name string, v uint32) {
		t.Helper()
		if v>>24 != 0x5a {
			t.Errorf("%s = %#08x: top byte %#02x, want 0x5a — this write would be"+
				" silently discarded", name, v, v>>24)
		}
	}

	check("LoadWDOG(0)", LoadWDOG(0))
	check("LoadWDOG(max)", LoadWDOG(TimeSet))
	check("LoadWDOG(overwide)", LoadWDOG(0xffffffff))
	check("StopRSTC()", StopRSTC())
	check("ArmRSTC(0)", ArmRSTC(0))
	check("ArmRSTC(all ones)", ArmRSTC(0xffffffff))
}

// A counter value must never reach the password field, whatever it is given.
func TestLoadWDOGCannotCorruptThePassword(t *testing.T) {
	for _, ticks := range []uint32{0, 1, TimeSet, TimeSet + 1, 0x00ffffff, 0xffffffff} {
		v := LoadWDOG(ticks)
		if v>>24 != 0x5a {
			t.Errorf("LoadWDOG(%#x) = %#08x, password corrupted", ticks, v)
		}
		if got, want := v&TimeSet, ticks&TimeSet; got != want {
			t.Errorf("LoadWDOG(%#x) counter field = %#x, want %#x", ticks, got, want)
		}
	}
}

// Arming is a read-modify-write: it owns the WRCFG field and nothing else,
// because RSTC carries reset-controller state this driver has no business
// changing.
//
// With one unavoidable exception, which this test exists partly to record. The
// top byte of a WRITE to this block is the password field, so whatever the
// register read back in bits [31:24] CANNOT be carried forward — the write must
// put 0x5a there instead. Read-modify-write on this register is therefore only
// meaningful over the low 24 bits, and an implementation that tried to preserve
// the top byte would produce writes the hardware discards.
func TestArmRSTCPreservesEverythingOutsideWRCFG(t *testing.T) {
	const (
		wrcfg   = ^uint32(WRCFGClr) // the bits ArmRSTC is entitled to change
		regBits = 0x00ffffff        // the bits a write can carry; [31:24] is the password
	)

	for _, cur := range []uint32{0x00000000, 0x00000abc, 0x00000102, 0x0fffffff, 0xffffffff} {
		v := ArmRSTC(cur)

		if v&WRCFGFullReset == 0 {
			t.Errorf("ArmRSTC(%#08x) = %#08x: WRCFG_FULL_RESET not set", cur, v)
		}

		got := v & regBits &^ wrcfg
		want := cur & regBits &^ wrcfg
		if got != want {
			t.Errorf("ArmRSTC(%#08x) = %#08x: bits outside WRCFG are %#x, want %#x",
				cur, v, got, want)
		}
	}
}

// Stopping must be Linux's bcm2835_wdt_stop value exactly. This is a constant
// worth pinning rather than trusting: an idle configuration that is subtly wrong
// leaves the reset controller armed.
func TestStopRSTCMatchesLinux(t *testing.T) {
	if got, want := StopRSTC(), uint32(0x5a000102); got != want {
		t.Errorf("StopRSTC() = %#08x, want %#08x", got, want)
	}
}

// Remaining is the inverse of TicksFor over the counter's range, and must mask
// off whatever else the register happens to carry.
func TestRemainingMasksAndConverts(t *testing.T) {
	if got, want := Remaining(0xdeadbeef), time.Duration(0xdeadbeef&TimeSet)*Period; got != want {
		t.Errorf("Remaining(0xdeadbeef) = %v, want %v", got, want)
	}
	if got := Remaining(0); got != 0 {
		t.Errorf("Remaining(0) = %v, want 0", got)
	}

	// Round-trip: a duration that converts to ticks converts back to itself,
	// modulo the tick quantum.
	for _, d := range []time.Duration{time.Second, 15 * time.Second, 100 * time.Millisecond} {
		ticks, ok := TicksFor(d)
		if !ok {
			t.Fatalf("TicksFor(%v) refused", d)
		}
		if got := Remaining(ticks); got != d {
			t.Errorf("Remaining(TicksFor(%v)) = %v", d, got)
		}
	}
}
