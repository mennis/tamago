// BCM2712 SoC watchdog timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"fmt"
	"sync"
	"time"
)

// Power Management, Reset controller and Watchdog registers, laid out as on
// the BCM2835 (see Linux drivers/watchdog/bcm2835_wdt.c and bcm2712-ds.dtsi).
const (
	// PM_OFFSET is the PM block offset from the SoC base, child address
	// 0x7d200000.
	PM_OFFSET = 0x1200000

	PM_RSTC = PM_OFFSET + 0x1c
	PM_RSTS = PM_OFFSET + 0x20
	PM_WDOG = PM_OFFSET + 0x24

	PM_PASSWORD = 0x5a000000

	PM_WDOG_TIME_SET         = 0x000fffff
	PM_RSTC_WRCFG_CLR        = 0xffffffcf
	PM_RSTC_WRCFG_FULL_RESET = 0x00000020
	PM_RSTC_RESET            = 0x00000102
)

// WDT represents a BCM2712 watchdog timer instance
type WDT struct {
	sync.Mutex

	timeout uint32
	running bool
}

// Watchdog is the BCM2712 watchdog timer instance, which resets the board
// unless [WDT.Reset] is called within the timeout given to [WDT.Start].
var Watchdog = &WDT{}

// Start the watchdog timer with a given timeout.
// The timeout resolution is 16µs and maximum is approximately 16 seconds.
func (w *WDT) Start(timeout time.Duration) error {
	w.Lock()
	defer w.Unlock()

	t := uint64(timeout) / WatchdogPeriod

	if (t & ^uint64(PM_WDOG_TIME_SET)) != 0 {
		return fmt.Errorf("watchdog timeout %v exceeds maximum (~16s)", timeout)
	}

	w.timeout = uint32(t)
	w.running = true

	w.reset()
	return nil
}

// Reset the watchdog countdown timer.
// Must be called periodically within the timeout period to prevent reset.
func (w *WDT) Reset() {
	w.Lock()
	defer w.Unlock()

	if !w.running {
		return
	}

	w.reset()
}

// reset performs the actual register writes (caller must hold lock)
func (w *WDT) reset() {
	rstc := Read32(SoCPeripheralAddress(PM_RSTC))
	wdog := PM_PASSWORD | (w.timeout & PM_WDOG_TIME_SET)

	// clear the read back top byte, any bit left there corrupts the password
	// and the write is silently discarded
	rstc = PM_PASSWORD | (rstc & PM_RSTC_WRCFG_CLR & 0x00ffffff) | PM_RSTC_WRCFG_FULL_RESET

	Write32(SoCPeripheralAddress(PM_WDOG), wdog)
	Write32(SoCPeripheralAddress(PM_RSTC), rstc)
}

// Stop the watchdog timer.
func (w *WDT) Stop() {
	w.Lock()
	defer w.Unlock()

	w.running = false
	Write32(SoCPeripheralAddress(PM_RSTC), PM_PASSWORD|PM_RSTC_RESET)
}

// Remaining returns the remaining duration before the watchdog fires.
func (w *WDT) Remaining() time.Duration {
	t := Read32(SoCPeripheralAddress(PM_WDOG)) & PM_WDOG_TIME_SET
	return time.Duration(uint64(t) * WatchdogPeriod)
}

// Running returns whether the watchdog is currently active.
func (w *WDT) Running() bool {
	w.Lock()
	defer w.Unlock()
	return w.running
}
