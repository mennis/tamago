// BCM2711 SoC support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package bcm2711 provides support to Go bare metal unikernels written using
// the TamaGo framework on BCM2711 SoCs (Raspberry Pi 4, Pi 400, CM4).
//
// This package is only meant to be used with `GOOS=tamago GOARCH=arm64` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
//
// The SoC has three address maps and this package uses the ARM view under Low
// Peripheral mode, which is how the Raspberry Pi firmware leaves it: the legacy
// master addresses of the form 0x7Enn_nnnn documented by the peripherals
// datasheet appear to the ARM at 0x0_FEnn_nnnn (§1.2.4), which the device tree
// and silicon both confirm.
package bcm2711

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/arm64"
	"github.com/usbarmory/tamago/internal/reg"
)

// GPU_BUS_OFFSET is the offset for CPU to GPU bus address translation, GPU bus
// address 0xC0000000 is ARM physical 0x00000000. The property tags mailbox
// channel is the documented exception to using it, see [Mailbox.Call].
const GPU_BUS_OFFSET uint32 = 0xC0000000

// GPUBusLimit is the size of the window the GPU alias covers, one gigabyte.
// Anything handed to the VideoCore must live below it: a buffer above is not
// slow, it is unreachable.
const GPUBusLimit uint64 = 0x40000000

// SysTimerFreq is the frequency (Hz) of the free running system timer, fixed at
// 1MHz on every SoC in this family.
const SysTimerFreq int64 = 1000000

// peripheralBase is the CPU visible base of the main peripheral window under
// Low Peripheral mode.
//
// It holds its value from package load rather than waiting for [Init]: the
// runtime drives MMIO backed drivers before Hwinit1, as schedinit reaches the
// RNG through randinit and samples nanotime, and a zero base sends both into
// low RAM with no way left to report it.
var peripheralBase uint64 = 0xfe000000

// PeripheralSize is the span of the main peripheral window.
const PeripheralSize uint64 = 0x01800000

// ARMLocalBase is a separate window and, unlike everything else here, an ARM
// only address rather than a translated legacy master address.
const ARMLocalBase uint64 = 0xff800000

// Peripheral block offsets from peripheralBase, that is the datasheet's legacy
// addresses with the 0x7e000000 removed, each cross checked against the device
// tree the firmware supplies.
const (
	SystemTimerOffset = 0x003000 // brcm,bcm2835-system-timer
	ASBOffset         = 0x00a000 // brcm,bcm2711-pm "asb", the AXI slave bridges
	ARMCOffset        = 0x00b000 // SP804 timer, doorbells, VPU mailboxes
	MailboxOffset     = 0x00b880 // VideoCore mailboxes, within ARMC
	PMOffset          = 0x100000 // brcm,bcm2711-pm, reset controller and watchdog
	CPRMANOffset      = 0x101000 // brcm,bcm2711-cprman, clocks
	RNGOffset         = 0x104000 // brcm,bcm2711-rng200
	GPIOOffset        = 0x200000 // brcm,bcm2711-gpio
	UART0Offset       = 0x201000 // arm,pl011
	AUXOffset         = 0x215000 // mini UART and aux SPI
	EMMC2Offset       = 0x340000

	// Display pipeline. The peripherals datasheet omits the HVS and the pixel
	// valves entirely, so these are the device tree's, read back on silicon.
	HVSOffset   = 0x400000 // brcm,bcm2711-hvs
	PV0Offset   = 0x206000
	PV1Offset   = 0x207000
	PV2Offset   = 0x20a000 // drives HDMI0
	PV3Offset   = 0xc12000 // drives the VEC
	PV4Offset   = 0x216000 // drives HDMI1
	HDMI0Offset = 0xf00000 // nine sub-ranges, see below
	HDMI1Offset = 0xf05000

	// V3D, likewise absent from the datasheet. The two ranges are not one
	// block: core 0 is 0x4000 above the hub and each range is 0x4000 long, so
	// a driver assuming a contiguous region puts every core access inside the
	// hub. The MMU registers at +0x1200 are hub registers despite lying
	// between them.
	//
	// Reading these without the V3D clock is unproven, and this SoC has form
	// for hanging the bus on an unclocked block. Establish the clock with
	// SetClockState first.
	V3DOffset     = 0xc00000 // brcm,2711-v3d hub
	V3DCoreOffset = 0x004000 // core 0, relative to the hub
	V3DCoreSize   = 0x004000
)

// HDMI0 sub-block offsets. The controller is nine register ranges scattered
// across the peripheral window rather than one block, and HD lies outside the
// 0xf00000 range the name suggests, so an assumption of contiguity computes
// plausible addresses for eight of them and reads a neighbour for the ninth.
//
// These are confirmed for reading only. Nothing has written through them, and
// reading with the HDMI clocks gated does not return, so a consumer must
// establish that the block is clocked first.
const (
	HDMI0CoreOffset     = 0xf00700
	HDMI0DVPOffset      = 0xf00300
	HDMI0PHYOffset      = 0xf00f00
	HDMI0RMOffset       = 0xf00f80
	HDMI0CSCOffset      = 0xf00200
	HDMI0PacketOffset   = 0xf01b00
	HDMI0MetadataOffset = 0xf01f00
	HDMI0CECOffset      = 0xf04300
	HDMI0HDOffset       = 0xf20000
)

// ARM processor instance.
//
// TimerMultiplier is deliberately left zero and the generic timers are not
// initialized. This SoC tells time with the free running system timer, which
// timer.go reads and scales itself because that counter is correct from reset
// and the runtime samples Nanotime before Hwinit1 runs.
//
// A multiplier derived from SysTimerFreq would describe the wrong clock, as
// every arm64.CPU method that consults it applies it to CNTPCT, running at
// CNTFRQ rather than 1MHz. Zero is the fail closed value: SetTime and SetAlarm
// return early on it, which is the right answer for a SoC whose generic timer
// was never set up.
//
// Wiring the generic timer up is not a small change, as TimerOffset is shared:
// nanotime adds it to a system timer reading while SetTime computes it from
// CNTPCT, so a board wanting settable time needs an offset expressed against
// the counter nanotime actually reads.
var ARM = &arm64.CPU{}

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100000 // 1 MB

// Init initializes the BCM2711 SoC at the given peripheral base, which is a
// parameter because the ARM view of the peripherals depends on whether the VPU
// enabled Low Peripheral mode and only a board package knows.
func Init(base uint64) {
	peripheralBase = base
	ARM.Init()
}

// PeripheralAddress returns the CPU visible address of a peripheral block
// offset. An offset beyond the window panics rather than returning an address
// that silently reads as zeroes.
func PeripheralAddress(offset uint64) uint64 {
	if offset >= PeripheralSize {
		panic("bcm2711: peripheral offset outside the window")
	}

	return peripheralBase + offset
}

// Read32 reads a 32-bit peripheral register.
func Read32(addr uint64) uint32 {
	return reg.Read32At(addr)
}

// Write32 writes a 32-bit peripheral register.
func Write32(addr uint64, val uint32) {
	reg.Write32At(addr, val)
}
