// BCM2711 SoC watchdog timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"fmt"
	"github.com/usbarmory/tamago/soc/bcm2711/pmwdt"
	"sync"
	"time"
)

// Power Management, Reset controller and Watchdog registers.
//
// This is the PM block watchdog every BCM SoC in this family carries
// (brcm,bcm2835-pm-wdt), not an SP805: a driver probing for the latter finds
// nothing and reports no watchdog, which is wrong rather than disappointing.
const (
	// PMRSTC, PMRSTS and PMWDOG are absolute peripheral offsets (PMOffset
	// relative), passed directly to PeripheralAddress.
	PMRSTC = PMOffset + 0x1c // reset control
	PMRSTS = PMOffset + 0x20 // reset status: why the board last came up
	PMWDOG = PMOffset + 0x24 // countdown value

	// PMPassword must occupy the top byte of EVERY write to this block.
	// Omit it and the write is silently discarded — no error, the register
	// just does not change, which is the failure mode most likely to be
	// mistaken for a dead watchdog rather than a missing password.
	PMPassword = 0x5a000000

	// PMWDOGTimeSet masks the 20-bit down-counter field. At the fixed 16µs
	// tick this block runs at, the longest armable window is ~16.7s.
	PMWDOGTimeSet = 0x000fffff

	// PMRSTCWRCFGClr clears the WRCFG field; PMRSTCWRCFGFullReset selects a
	// full reset as the action taken when the counter reaches zero.
	PMRSTCWRCFGClr       = 0xffffffcf
	PMRSTCWRCFGFullReset = 0x00000020

	// PMRSTCReset returns the reset controller to its idle configuration,
	// which is how the watchdog is stopped.
	PMRSTCReset = 0x00000102
)

// WDT represents a BCM2711 PM-block watchdog timer instance.
type WDT struct {
	sync.Mutex

	ticks   uint32
	running bool
}

// Watchdog is the BCM2711 watchdog timer instance, typically used to reset the
// board on a lock-up such as an out-of-memory halt. Start it with a timeout and
// call Reset within that timeout, or the board resets.
var Watchdog = &WDT{}

// Start the watchdog timer with a given timeout.
// The timeout resolution is 16µs and the maximum is approximately 16.7 seconds.
func (w *WDT) Start(timeout time.Duration) error {
	w.Lock()
	defer w.Unlock()

	ticks, ok := pmwdt.TicksFor(timeout)
	if !ok {
		return fmt.Errorf("bcm2711: watchdog timeout %v does not fit the 20-bit/16µs counter (max ~16.7s)", timeout)
	}

	w.ticks = ticks
	w.running = true

	w.arm()
	return nil
}

// arm performs the actual register writes (caller must hold the lock).
//
// The counter is loaded BEFORE the reset controller is told to act on it.
// Enabling first would expose a window in which whatever value WDOG happens
// to already hold - possibly a small leftover one - is live and counting.
func (w *WDT) arm() {
	Write32(PeripheralAddress(PMWDOG), pmwdt.LoadWDOG(w.ticks))

	rstc := Read32(PeripheralAddress(PMRSTC))
	Write32(PeripheralAddress(PMRSTC), pmwdt.ArmRSTC(rstc))
}

// Reset the watchdog countdown timer.
// Must be called periodically within the timeout period to prevent reset.
func (w *WDT) Reset() {
	w.Lock()
	defer w.Unlock()

	if !w.running {
		return
	}

	Write32(PeripheralAddress(PMWDOG), pmwdt.LoadWDOG(w.ticks))
}

// Stop the watchdog timer.
func (w *WDT) Stop() {
	w.Lock()
	defer w.Unlock()

	w.running = false
	Write32(PeripheralAddress(PMRSTC), pmwdt.StopRSTC())
}

// Remaining returns the remaining duration before the watchdog fires.
//
// Reading it twice and seeing it fall is the only direct evidence the
// watchdog is actually running, as opposed to having accepted writes that
// went nowhere because the password byte was dropped somewhere upstream.
func (w *WDT) Remaining() time.Duration {
	t := Read32(PeripheralAddress(PMWDOG)) & PMWDOGTimeSet
	return time.Duration(uint64(t) * WatchdogPeriod)
}

// Running returns whether the watchdog is currently active.
func (w *WDT) Running() bool {
	w.Lock()
	defer w.Unlock()
	return w.running
}

// ResetStatus returns the raw RSTS register, which records why the board
// last came up. The firmware encodes the boot partition in this register,
// and a watchdog reset leaves a distinguishable value, so a boot can tell
// whether it followed a watchdog firing rather than a power cycle.
func (w *WDT) ResetStatus() uint32 {
	return Read32(PeripheralAddress(PMRSTS))
}
