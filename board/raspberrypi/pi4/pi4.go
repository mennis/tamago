// Raspberry Pi 4 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package pi4 provides hardware initialization, automatically on import, for
// the Raspberry Pi 4 Model B, Pi 400 and CM4.
//
// This package is only meant to be used with `GOOS=tamago GOARCH=arm64` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
//
// The firmware enters the image at EL2 with the MMU and caches already on and
// the flattened device tree pointer in x0, which is the only route to a device
// tree on this board: a program that wants it must take the pointer before the
// runtime overwrites x0. The image is loaded at kernel_address, 0x80000, so
// runtime/goos.RamStart is the load address itself, which is what
// soc/bcm2711/mem.go's scratch window exists to work around.
//
// The console is PL011 UART0 on GPIO14/15 at 115200 8N1. It also requires
// `dtoverlay=disable-bt` in config.txt, without which the firmware wires the
// PL011 to Bluetooth and puts the mini UART on those pins, and the board then
// boots silently with no visible fault.
package pi4

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/soc/bcm2711"
)

// peripheralBase is the ARM view of the main peripheral window under Low
// Peripheral mode, confirmed against this board's device tree and by the PL011
// answering where the translation says it should.
const peripheralBase = 0xfe000000

const (
	// UARTClock is the PL011 reference clock, the firmware's nominal 48MHz.
	// It lives here rather than in the SoC package because CPRMAN and the
	// firmware decide it and a config.txt that moves core_freq can move it,
	// which is why the driver takes it as a parameter.
	UARTClock = 48000000

	// UARTBaud is the console rate. 115200 is what the firmware itself uses
	// with enable_uart=1, so one terminal follows the board from the
	// firmware's own messages straight into Go without changing speed.
	UARTBaud = 115200
)

// InheritedGPFSEL1 is GPFSEL1 exactly as this board's firmware (or, after a warm
// reboot out of Linux, its pinctrl driver) left it, sampled at Hwinit1 entry
// before ConfigurePins overwrites it. Zero until Init has run.
//
// EarlyConsoleUsable decodes the part that matters.
var InheritedGPFSEL1 uint32

// EarlyConsoleUsable reports whether GPIO14/15 were already muxed to ALT0 when
// this board entered Hwinit1, that is whether output produced before Hwinit1
// could reach the header pins.
//
// The window is not empty: hwinit0, osinit and the whole of schedinit run
// first, covering the RNG driver and every panic it can raise. When this
// returns false a board dying in that window dies without saying so.
//
// Nothing here can widen the window: goos.Hwinit0 is claimed by package arm64,
// which uses it to build the flat MMU map, and the linkname admits one
// definition. Giving a board console output during schedinit means adding a
// board hook to arm64's Hwinit0, which is shared code and a larger change than
// this port should make on its own.
func EarlyConsoleUsable() bool {
	// GPFSEL1 covers pins 10-19 at three bits each, so pin 14 is at bit 12 and
	// pin 15 at bit 15. ALT0 is 0b100.
	const alt0 = 0b100

	return (InheritedGPFSEL1>>12)&0b111 == alt0 &&
		(InheritedGPFSEL1>>15)&0b111 == alt0
}

// Init takes care of the lower level initialization triggered early in runtime
// setup (post World start). The order is not arbitrary.
//
// bcm2711.Init restates the peripheral base and installs the exception vector
// table; MMIO backed drivers have already run by then, which is why the base is
// valid from package load. The MMU and caches follow, rebuilding the tables to
// put the board in a known state rather than depending on what an earlier phase
// left, and discarding the lines the firmware left behind before the data cache
// comes on. Only then are the console pins muxed and the PL011 configured.
//
//go:linkname Init runtime/goos.Hwinit1
func Init() {
	bcm2711.Init(peripheralBase)

	bcm2711.ARM.InitMMU()
	bcm2711.ARM.EnableCache()

	// Sample GPFSEL1 before overwriting it, so that a boot can report what
	// the firmware left rather than leaving it to inference. See
	// [EarlyConsoleUsable] for what the answer decides.
	InheritedGPFSEL1 = bcm2711.Read32(bcm2711.PeripheralAddress(
		bcm2711.GPIOOffset + bcm2711.GPFSEL1))

	// Mux GPIO14/15 to ALT0 before touching the PL011, rather than trusting
	// what the firmware or a prior pinctrl driver left in GPFSEL1. Measured
	// as necessary on hardware: without it the console is silent and nothing
	// says why.
	//
	// The error is unreachable for these two pins, but discarding it would let
	// a later change to the pin list fail into exactly that silent state.
	if err := bcm2711.ConfigurePins(); err != nil {
		panic("pi4: UART0 pin mux failed: " + err.Error())
	}

	// bcm2711.UART0 directly, NOT this package's UART0 alias.
	//
	// Hwinit1 runs after schedinit and before runtime.main, and therefore
	// before package-level initialization. bcm2711.UART0 survives that because
	// `&UART{}` is a composite literal the linker can place statically; the
	// alias here does not, because copying another package's variable needs a
	// generated init function that has not run yet. Using it left a nil
	// receiver, and `hw.base = ...` at the top of Init stored through it — an
	// abort at uart.go:108, from a board that had otherwise come up correctly.
	//
	// soc/bcm2712's uart.go documents the same class of failure from the other
	// direction: a base address computed by a function call in a var
	// initializer read as zero and "faulted into the not-yet-installed
	// exception vector, hanging at PC 0x200".
	bcm2711.UART0.Init(UARTBaud, UARTClock)
}
