// BCM2711 SoC timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"time"
	_ "unsafe"
)

// WatchdogPeriod is the fixed tick of the PM block watchdog counter, see
// watchdog.go. Every BCM SoC in this family carries the same one, as none of
// them clock the block from anything configurable.
const WatchdogPeriod = uint64(16 * time.Microsecond)

// refFreq is the resolution runtime/goos.Nanotime reports in.
const refFreq int64 = 1e9

// read_systimer reads the free running system timer as a 64-bit microsecond
// counter. Defined in timer.s, which handles the tear between the two halves:
// the hardware does not latch them together, so a read straddling a low word
// rollover can pair a high word that has already incremented with a low word
// that has already wrapped.
func read_systimer() int64

// Busyloop spins for approximately count iterations. It exists for callers
// that must not sleep or yield, such as rng.go, which runs before the
// scheduler is up. Defined in timer.s.
func Busyloop(count uint32)

// nanotime backs runtime/goos.Nanotime, which the runtime calls before Hwinit1.
// It needs no guard against an uninitialized peripheral base, which is why that
// base holds its value from package load.
//
//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 {
	return read_systimer()*(refFreq/SysTimerFreq) + ARM.TimerOffset
}
