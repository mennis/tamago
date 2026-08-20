// BCM2711 GIC-400 support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"fmt"
	"sync/atomic"

	"github.com/usbarmory/tamago/arm/gic"
)

// GIC-400 base, as an offset from ARMLocalBase rather than from the peripheral
// window: the device tree places interrupt-controller@40041000 in the ARM local
// window, which /soc ranges maps from child 0x40000000 to CPU 0xff800000. The
// distributor therefore resolves to 0xff841000 and the CPU interface, one page
// further as [gic.GIC] expects, to 0xff842000.
const GIC_OFFSET = 0x40000

// V3DIRQ is the GIC interrupt ID of the V3D GPU. The device tree node carries
// interrupts = <0x0 0x4a 0x4>, an SPI numbered 74, and an SPI's ID is its
// number plus 32.
//
// It is one interrupt shared by the V3D hub and core, so a handler must service
// both status registers or a hub condition nothing acknowledges holds the line
// asserted.
const V3DIRQ = 106

// GIC is the BCM2711 GIC-400 instance, populated by [InitGIC].
var GIC = &gic.GIC{}

// irqHandlers maps an interrupt ID to its handler. Registration happens before
// the servicing goroutine starts, so it is not synchronized; servicing enforces
// that (see RegisterInterrupt).
var (
	irqHandlers = make(map[int]func())
	servicing   atomic.Bool
)

// InitGIC initializes the GIC-400 distributor and this core's CPU interface. It
// must run after [Init] and before interrupts are enabled.
//
// The interrupts are moved to Group 1 and the priority mask raised to match,
// as TamaGo runs Non-Secure on this SoC; SPIs are targeted at CPU 0. The GIC
// window needs no MMU work, it lies outside both the RAM region and the
// reserved window and is therefore mapped as Device.
func InitGIC() {
	GIC.Base = ARMLocalBase + GIC_OFFSET
	GIC.Init()

	GIC.SetInterruptsGroup(true)
	GIC.SetInterruptsPriority(0xa0)
	GIC.SetInterruptsTarget(1 << 0)
	GIC.SetPriorityMask(0xff)
}

// RegisterInterrupt sets fn as the handler for interrupt id, configures it
// level-sensitive and enables it. Handlers run in the servicing goroutine (see
// [ServiceInterrupts]) and must be registered before it starts, calling this
// afterwards panics rather than racing the handler map.
//
// A handler must quiesce its source before returning: the CPU interface runs
// with EOImodeNS clear, so acknowledging also deactivates, and a level source
// left asserted re-pends immediately.
//
// Level is configured explicitly rather than left as found. Every SPI on this
// SoC is a native peripheral line, and an edge configuration on a line already
// asserted at unmask time never delivers its first interrupt. Use
// [gic.GIC.SetInterruptConfig] directly for the exceptional case.
func RegisterInterrupt(id int, fn func()) {
	if servicing.Load() {
		panic("bcm2711: RegisterInterrupt after ServiceInterrupts")
	}

	irqHandlers[id] = fn

	GIC.SetInterruptConfig(id, false)
	GIC.EnableInterrupt(id)
}

// maxIRQRepeats bounds how many times serviceIRQ dispatches one interrupt ID
// back to back before treating the source as stuck, turning a silent livelock
// into a diagnosable panic. It is far above any legitimate burst.
const maxIRQRepeats = 1 << 20

// serviceIRQ drains every pending interrupt, acknowledging each at the GIC and
// dispatching it to its registered handler.
func serviceIRQ() {
	last, repeats := -1, 0

	for {
		id := GIC.GetInterrupt()

		if id >= 1020 {
			// 1023 is spurious, nothing left pending
			return
		}

		if id == last {
			if repeats++; repeats >= maxIRQRepeats {
				panic(fmt.Sprintf("bcm2711: interrupt %d stuck, handler did not quiesce its source", id))
			}
		} else {
			last, repeats = id, 0
		}

		if fn := irqHandlers[id]; fn != nil {
			fn()
		}
	}
}

// ServiceInterrupts unmasks CPU interrupts and services GIC interrupts until
// the program exits. It blocks and is normally run in its own goroutine. No
// further [RegisterInterrupt] is permitted once it starts.
func ServiceInterrupts() {
	servicing.Store(true)
	ARM.ServiceInterrupts(serviceIRQ)
}
