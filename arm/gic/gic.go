// ARM Generic Interrupt Controller (GICv2) driver
// https://github.com/usbarmory/tamago
//
// IP: ARM Generic Interrupt Controller version 2.0
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package gic implements a driver for the ARM Generic Interrupt Controller
// (GICv2).
//
// The driver is based on the following reference specifications:
//   - ARM IHI 0048B.b - ARM Generic Interrupt Controller - Architecture version 2.0
//
// GICv2 is not specific to 32-bit ARM, arm64 parts ship it as well, therefore
// this package is meant to be used with `GOOS=tamago` on both `GOARCH=arm` and
// `GOARCH=arm64` as supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
package gic

import (
	"github.com/usbarmory/tamago/internal/reg"
)

// GICv2 registers
const (
	// GIC offsets in Cortex-A7
	// (p178, Table 8-1, Cortex-A7 MPCore Technical Reference Manual),
	// a GIC-400 uses the same layout.
	GICD_OFF = 0x1000
	GICC_OFF = 0x2000

	// Distributor register map
	// (p75, Table 4-1, ARM Generic Interrupt Controller Architecture Specification).
	GICD_CTLR       = 0x000
	CTLR_ENABLEGRP1 = 1
	CTLR_ENABLEGRP0 = 0

	GICD_TYPER    = 0x004
	TYPER_ITLINES = 0

	GICD_IGROUPR   = 0x080
	GICD_ISENABLER = 0x100
	GICD_ICENABLER = 0x180
	GICD_ICPENDR   = 0x280

	// 1 byte per interrupt
	GICD_IPRIORITYR = 0x400
	GICD_ITARGETSR  = 0x800

	// 2 bits per interrupt, bit[1] selects edge-triggered
	GICD_ICFGR    = 0xc00
	ICFGR_EDGE    = 1
	ICFGR_PER_REG = 16

	// CPU interface register map
	// (p76, Table 4-2, ARM Generic Interrupt Controller Architecture Specification).
	GICC_CTLR  = 0x0000
	CTLR_FIQEN = 3

	GICC_PMR     = 0x0004
	PMR_PRIORITY = 0

	GICC_IAR = 0x000c
	IAR_ID   = 0

	GICC_EOIR = 0x0010
	EOIR_ID   = 0

	GICC_AIAR = 0x0020
	AIAR_ID   = 0

	GICC_AEOIR = 0x0024
	AEOIR_ID   = 0
)

// SPI is the first Shared Peripheral Interrupt ID, the SGIs and PPIs below it
// have read-only target and configuration fields.
const SPI = 32

// GIC represents a Generic Interrupt Controller (GICv2) instance.
type GIC struct {
	// Base register, 64-bit as a GICv2 is not necessarily addressable in 32
	// bits (e.g. the BCM2712 places its GIC-400 above 4 GiB).
	Base uint64

	// control registers
	gicd uint64
	gicc uint64
}

// set writes a single bit of a 32-bit register at a 64-bit address.
func set(addr uint64, pos int, val bool) {
	r := reg.Read32At(addr)

	if val {
		r |= 1 << pos
	} else {
		r &^= 1 << pos
	}

	reg.Write32At(addr, r)
}

// getN reads a masked field of a 32-bit register at a 64-bit address.
func getN(addr uint64, pos int, mask int) uint32 {
	return (reg.Read32At(addr) >> pos) & uint32(mask)
}

// lines returns the number of 32-interrupt blocks the distributor implements,
// counting the block of 32 internal (SGI and PPI) interrupts.
func (hw *GIC) lines() uint64 {
	return uint64(getN(hw.gicd+GICD_TYPER, TYPER_ITLINES, 0x1f)) + 1
}

// InitGIC initializes an ARM Generic Interrupt Controller (GICv2) instance.
func (hw *GIC) Init() {
	if hw.Base == 0 {
		panic("invalid GIC instance")
	}

	hw.gicd = hw.Base + GICD_OFF
	hw.gicc = hw.Base + GICC_OFF

	for n := uint64(0); n < hw.lines(); n++ {
		// Disable interrupts
		addr := hw.gicd + GICD_ICENABLER + 4*n
		reg.Write32At(addr, 0xffffffff)

		// Clear pending interrupts
		addr = hw.gicd + GICD_ICPENDR + 4*n
		reg.Write32At(addr, 0xffffffff)

		// Group 0 (Secure)
		addr = hw.gicd + GICD_IGROUPR + 4*n
		reg.Write32At(addr, 0x00000000)
	}

	// Set priority mask to allow Non-Secure world to use the lower half
	// of the priority range.
	reg.Write32At(hw.gicc+GICC_PMR, 0x80)

	set(hw.gicc+GICC_CTLR, CTLR_FIQEN, false)

	set(hw.gicc+GICC_CTLR, CTLR_ENABLEGRP1, true)
	set(hw.gicc+GICC_CTLR, CTLR_ENABLEGRP0, true)

	set(hw.gicd+GICD_CTLR, CTLR_ENABLEGRP1, true)
	set(hw.gicd+GICD_CTLR, CTLR_ENABLEGRP0, true)
}

// FIQEn controls whether Group 0 (Secure) interrupts should be signalled as
// IRQ or FIQ requests.
func (hw *GIC) FIQEn(fiq bool) {
	set(hw.gicc+GICC_CTLR, CTLR_FIQEN, fiq)
}

func (hw *GIC) irq(m int, enable bool) {
	addr := hw.gicd

	n := uint64(m / 32)
	i := m % 32

	if enable {
		addr += GICD_ISENABLER
	} else {
		addr += GICD_ICENABLER
	}

	// ISENABLER and ICENABLER are write-1-to-act, a read-modify-write would
	// act on every other set bit as well.
	reg.Write32At(addr+4*n, 1<<i)
}

// EnableInterrupt enables forwarding of the corresponding interrupt to the CPU
// and configures its group status (Secure: Group 0, Non-Secure: Group 1).
func (hw *GIC) EnableInterrupt(id int) {
	hw.irq(id, true)
}

// DisableInterrupt disables forwarding of the corresponding interrupt to the
// CPU.
func (hw *GIC) DisableInterrupt(id int) {
	hw.irq(id, false)
}

// GetInterrupt obtains and acknowledges a signaled interrupt.
func (hw *GIC) GetInterrupt() (id int) {
	m := getN(hw.gicc+GICC_IAR, IAR_ID, 0x3ff)

	if m < 1020 {
		reg.Write32At(hw.gicc+GICC_EOIR, (m&0x3ff)<<EOIR_ID)
	}

	return int(m)
}

// GetNonSecureInterrupt obtains and acknowledges a signaled Non-Secure
// interrupt from Secure access.
func (hw *GIC) GetNonSecureInterrupt() (id int) {
	m := getN(hw.gicc+GICC_AIAR, AIAR_ID, 0x3ff)

	if m < 1020 {
		reg.Write32At(hw.gicc+GICC_AEOIR, (m&0x3ff)<<AEOIR_ID)
	}

	return int(m)
}

// SetInterruptGroup assigns the corresponding interrupt to either Group 0
// (Secure, false) or 1 (Non-Secure, true).
func (hw *GIC) SetInterruptGroup(id int, status bool) {
	n := uint64(id / 32)
	i := id % 32

	set(hw.gicd+GICD_IGROUPR+4*n, i, status)
}

// SetInterruptsGroup assigns all interrupts to either Group 0 (Secure, false)
// or 1 (Non-Secure, true).
func (hw *GIC) SetInterruptsGroup(status bool) {
	// Group 0 (Secure)
	mask := uint32(0x00000000)

	if status {
		// Group 1 (Non-Secure)
		mask = 0xffffffff
	}

	for n := uint64(0); n < hw.lines(); n++ {
		reg.Write32At(hw.gicd+GICD_IGROUPR+4*n, mask)
	}
}

// SetPriorityMask sets the CPU interface priority mask, only interrupts of
// numerically lower priority than the mask are signalled. [GIC.Init] leaves it
// at 0x80, the lower half of the priority range.
func (hw *GIC) SetPriorityMask(prio uint8) {
	reg.Write32At(hw.gicc+GICC_PMR, uint32(prio))
}

// SetInterruptPriority sets the priority of the corresponding interrupt,
// numerically lower is higher priority.
func (hw *GIC) SetInterruptPriority(id int, prio uint8) {
	addr := hw.gicd + GICD_IPRIORITYR + 4*uint64(id/4)
	pos := 8 * (id % 4)

	r := reg.Read32At(addr)
	r &^= 0xff << pos
	r |= uint32(prio) << pos

	reg.Write32At(addr, r)
}

// SetInterruptsPriority sets the priority of every interrupt.
func (hw *GIC) SetInterruptsPriority(prio uint8) {
	mask := uint32(prio)
	mask |= mask << 8
	mask |= mask << 16

	// 4 interrupts per register
	for n := uint64(0); n < hw.lines()*32/4; n++ {
		reg.Write32At(hw.gicd+GICD_IPRIORITYR+4*n, mask)
	}
}

// SetInterruptTarget sets the CPU interfaces, as a bitmask of up to 8 targets,
// the corresponding interrupt is forwarded to. Only SPIs can be targeted.
func (hw *GIC) SetInterruptTarget(id int, targets uint8) {
	addr := hw.gicd + GICD_ITARGETSR + 4*uint64(id/4)
	pos := 8 * (id % 4)

	r := reg.Read32At(addr)
	r &^= 0xff << pos
	r |= uint32(targets) << pos

	reg.Write32At(addr, r)
}

// SetInterruptsTarget sets the CPU interfaces, as a bitmask of up to 8
// targets, every SPI is forwarded to.
func (hw *GIC) SetInterruptsTarget(targets uint8) {
	mask := uint32(targets)
	mask |= mask << 8
	mask |= mask << 16

	// 4 interrupts per register, skipping the read-only SGI and PPI fields
	for n := uint64(SPI / 4); n < hw.lines()*32/4; n++ {
		reg.Write32At(hw.gicd+GICD_ITARGETSR+4*n, mask)
	}
}

// SetInterruptConfig configures the corresponding interrupt as edge-triggered
// (true) or level-sensitive (false). Only SPIs can be configured.
//
// [GIC.Init] leaves the configuration as found, a mismatch is silent: an edge
// source configured as level never clears, a level source configured as edge
// yields no edge when it is already asserted at unmask time and therefore
// never delivers its first interrupt.
func (hw *GIC) SetInterruptConfig(id int, edge bool) {
	addr := hw.gicd + GICD_ICFGR + 4*uint64(id/ICFGR_PER_REG)
	pos := 2*(id%ICFGR_PER_REG) + ICFGR_EDGE

	set(addr, pos, edge)
}
