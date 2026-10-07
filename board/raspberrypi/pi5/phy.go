// Raspberry Pi 5 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pi5

import (
	"time"
	"unsafe"

	"github.com/usbarmory/tamago/phy/broadcom/bcm54213"
	"github.com/usbarmory/tamago/soc/bcm2712"
)

// The BCM54213PE reset line is RP1 GPIO 32, active-low. The bootloader leaves
// the pin unclaimed with its pad pull-down enabled, holding the PHY in reset
// (it answers all-ones on MDIO) until the line is released.
const (
	rp1IOBank1  = 0x1f000d4000
	rp1RIOBank1 = 0x1f000e4000
	rp1PadBank1 = 0x1f000f4000

	// GPIO 32 is index 4 within bank1 (which starts at GPIO 28).
	gpio32Idx  = 32 - 28
	gpio32Ctrl = rp1IOBank1 + gpio32Idx*8 + 4
	gpio32Pad  = rp1PadBank1 + 4 + gpio32Idx*4

	gpio32RIOBit = 1 << gpio32Idx

	funcselRIO  = 5    // sys_rio
	funcselMask = 0x1f // GPIOx_CTRL FUNCSEL [4:0]

	padOD  = 1 << 7 // output disable
	padPDE = 1 << 2 // pull-down enable
)

func w32(addr uintptr, v uint32) { *(*uint32)(unsafe.Pointer(addr)) = v }
func r32(addr uintptr) uint32    { return *(*uint32)(unsafe.Pointer(addr)) }

// phyReset drives the BCM54213PE active-low reset line, true pulls it low and
// false releases it.
//
// The line must go from pulled-low to driven-low without floating, so the RIO
// output is staged behind the pad output disable, the pin is routed to
// sys_rio, and a single pad write then enables the driver and drops the
// pull-down together.
func phyReset(assert bool) {
	if assert {
		// Stage RIO low + output-enabled while the pad still blocks the driver.
		w32(rp1RIOBank1, r32(rp1RIOBank1)&^uint32(gpio32RIOBit))
		w32(rp1RIOBank1+4, r32(rp1RIOBank1+4)|gpio32RIOBit)

		// Route the pin to sys_rio, preserving the filter constant.
		w32(gpio32Ctrl, (r32(gpio32Ctrl)&^uint32(funcselMask))|funcselRIO)
		dsb()

		// One write: enable the driver, drop the pull-down.
		w32(gpio32Pad, r32(gpio32Pad)&^uint32(padOD|padPDE))
		dsb()

		return
	}

	w32(rp1RIOBank1, r32(rp1RIOBank1)|gpio32RIOBit)
	dsb()
}

// dsb issues a full system data synchronization barrier, to keep the pad and
// RIO writes of the reset sequence in order.
func dsb() { bcm2712.ARM.DataSynchronizationBarrier() }

// EnablePHYReset installs the board GPIO 32 reset hook as
// [bcm54213.PHY.ResetLine], and a [bcm54213.PHY.Sleep] which feeds the
// watchdog, on the RP1 GEM PHY driver and returns it ready for Init.
func EnablePHYReset() *bcm54213.PHY {
	bcm2712.EthernetPHY.ResetLine = phyReset
	bcm2712.EthernetPHY.Sleep = func(d time.Duration) {
		// reset settling and autonegotiation polling can take seconds
		Watchdog.Reset()
		time.Sleep(d)
		Watchdog.Reset()
	}

	return bcm2712.EthernetPHY
}
