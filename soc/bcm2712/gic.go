// BCM2712 GIC-400 (GICv2) interrupt controller support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"fmt"
	"sync/atomic"

	"github.com/usbarmory/tamago/arm/gic"
)

// GIC-400 base, as an offset from the SoC peripheral base. [gic.GIC] places
// the distributor one page above it, at 0x10_7fff9000, and the CPU interface
// at 0x10_7fffa000.
const GIC_OFFSET = 0x03ff8000

// GIC is the BCM2712 GIC-400 instance, populated by [InitGIC].
var GIC = &gic.GIC{}

// irqHandlers maps an interrupt ID to its handler. It is unsynchronized, so
// registration must precede ServiceInterrupts; servicing makes a later
// RegisterInterrupt panic rather than race the map.
var (
	irqHandlers = make(map[int]func())
	servicing   atomic.Bool
)

// InitGIC initializes the GIC-400 distributor and this core's CPU interface. It
// must run after [Init] and before interrupts are enabled.
//
// The interrupts are moved to Group 1 and the priority mask raised to match,
// as TamaGo runs Non-Secure on this SoC; SPIs are targeted at CPU 0.
func InitGIC() {
	GIC.Base = SoCPeripheralAddress(GIC_OFFSET)
	GIC.Init()

	GIC.SetInterruptsGroup(true)
	GIC.SetInterruptsPriority(0xa0)
	GIC.SetInterruptsTarget(1 << 0)
	GIC.SetPriorityMask(0xff)
}

// RegisterInterrupt sets fn as the handler for interrupt id and enables it in
// the GIC. It panics if called after [ServiceInterrupts].
//
// A handler for a level-sensitive interrupt must quiesce its source before
// returning, or the line re-pends; RP1 MSI vectors are edge-triggered.
func RegisterInterrupt(id int, fn func()) {
	if servicing.Load() {
		panic("bcm2712: RegisterInterrupt after ServiceInterrupts")
	}
	irqHandlers[id] = fn
	GIC.EnableInterrupt(id)
}

// maxIRQRepeats bounds back-to-back dispatches of the same interrupt id. As
// GetInterrupt performs the combined EOI (EOImodeNS=0), an unquiesced level
// source re-pends at once; the bound turns that livelock into a panic.
const maxIRQRepeats = 1 << 20

// serviceIRQ acknowledges and dispatches every pending interrupt.
func serviceIRQ() {
	last, repeats := -1, 0

	for {
		id := GIC.GetInterrupt()
		if id >= 1020 {
			// 1023 = spurious / nothing left pending.
			return
		}

		if id == last {
			if repeats++; repeats >= maxIRQRepeats {
				panic(fmt.Sprintf("bcm2712: GIC interrupt %d stuck: level handler did not quiesce its source", id))
			}
		} else {
			last, repeats = id, 0
		}

		if fn := irqHandlers[id]; fn != nil {
			fn()
		}
	}
}

// ServiceInterrupts unmasks CPU interrupts and dispatches GIC interrupts to
// their registered handlers, it never returns and is normally run in its own
// goroutine.
func ServiceInterrupts() {
	servicing.Store(true)
	ARM.ServiceInterrupts(serviceIRQ)
}
