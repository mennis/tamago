// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

import (
	"math"

	"github.com/usbarmory/tamago/internal/reg"
)

// ARM timer register constants
// (ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile)
const (
	// p6721, Table 12-2
	CNTCR = 0
	// base frequency
	CNTFID0 = 0x20

	// p6855, I5.7.2 CNTCR, Counter Control Register
	CNTCR_FCREQ = 8
	CNTCR_HDBG  = 1
	CNTCR_EN    = 0

	CNTKCTL_PL0PCTEN = 0

	// nanoseconds
	refFreq int64 = 1e9
)

// Timer interrupts, the private peripheral interrupt (PPI) INTIDs the generic
// timers raise (ARM IHI 0069G, Table 2-1).
const (
	// EL1 physical timer, CNTP
	PHYSICAL_TIMER_IRQ = 30
	// EL1 virtual timer, CNTV
	VIRTUAL_TIMER_IRQ = 27

	// Deprecated: it names one of the two timers while claiming to name the
	// timer, use [CPU.TimerIRQ].
	TIMER_IRQ = PHYSICAL_TIMER_IRQ
)

// TimerType selects which of the two EL1 generic timers a core drives.
//
// A compare register compares against its own counter, so the counter, the
// compare and the PPI have to move together. The physical timer is correct
// where the payload owns CNTVOFF_EL2; a guest whose host owns it, and will not
// say what it is, must drive the virtual timer instead.
type TimerType int

const (
	PhysicalTimer TimerType = iota
	VirtualTimer
)

// defined in timer.s
func read_cntfrq() uint32
func write_cntkctl(val uint32)
func read_cntpct() uint64
func write_cntptval(val uint32, enable bool)
func read_cntvct() uint64
func write_cntvtval(val uint32, enable bool)

// InitGenericTimers initializes ARMv8 Generic Timers.
func (cpu *CPU) InitGenericTimers(base uint32, freq uint32) {
	if freq != 0 {
		if base != 0 {
			reg.Write(base+CNTFID0, freq)

			// set system counter to base frequency
			reg.Set(base+CNTCR, CNTCR_FCREQ)
			// stop system counter on debug
			reg.Set(base+CNTCR, CNTCR_HDBG)
			// start system counter
			reg.Set(base+CNTCR, CNTCR_EN)
		}

		// grant PL0 access
		write_cntkctl(1 << CNTKCTL_PL0PCTEN)
	}

	cpu.TimerMultiplier = float64(refFreq) / float64(read_cntfrq())
}

// Counter returns the count of the generic timer the core drives, CNTPCT or
// CNTVCT.
func (cpu *CPU) Counter() uint64 {
	if cpu.Timer == VirtualTimer {
		return read_cntvct()
	}

	return read_cntpct()
}

// TimerIRQ returns the PPI INTID raised by the generic timer the core drives.
func (cpu *CPU) TimerIRQ() int {
	if cpu.Timer == VirtualTimer {
		return VIRTUAL_TIMER_IRQ
	}

	return PHYSICAL_TIMER_IRQ
}

// GetTime returns the system time in nanoseconds.
func (cpu *CPU) GetTime() int64 {
	return int64(float64(cpu.Counter())*cpu.TimerMultiplier) + cpu.TimerOffset
}

// SetTime adjusts the system time to the argument nanoseconds value.
func (cpu *CPU) SetTime(ns int64) {
	if cpu.TimerMultiplier == 0 {
		return
	}

	cpu.TimerOffset = ns - int64(float64(cpu.Counter())*cpu.TimerMultiplier)
}

// SetAlarm sets the generic timer the core drives to the absolute time matching
// the argument nanoseconds value, an interrupt is generated at expiration.
func (cpu *CPU) SetAlarm(ns int64) {
	arm := write_cntptval

	if cpu.Timer == VirtualTimer {
		arm = write_cntvtval
	}

	if ns == 0 {
		arm(0, false)
		return
	}

	if cpu.TimerMultiplier == 0 {
		return
	}

	// The multiplier is nanoseconds-per-tick and is whole only when CNTFRQ
	// divides 1e9, so the division is done in float64: uint64() would turn
	// 41.666 into 41. The deadline is absolute, so the resulting error
	// scales with the counter rather than with the requested interval.
	set := uint64(float64(ns) / cpu.TimerMultiplier)
	now := cpu.Counter()
	cnt := set - now

	if set <= now {
		cnt = 1
	} else if cnt > math.MaxInt32 {
		cnt = math.MaxInt32
	}

	arm(uint32(cnt), true)
}

// Idle suspends execution until an interrupt is received tracking idle time
// for reporting by [CPU.IdleTime].
func (cpu *CPU) Idle() {
	start := read_cntpct()
	wfi()
	cpu.idle += read_cntpct() - start
}

// IdleTime returns the cumulative time spent halted, in nanoseconds. along
func (cpu *CPU) IdleTime() int64 {
	return int64(float64(cpu.idle) * cpu.TimerMultiplier)
}
