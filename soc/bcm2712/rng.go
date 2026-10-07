// BCM2712 SoC Random Number Generator (RNG) driver
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
	_ "unsafe"

	"github.com/usbarmory/tamago/internal/rng"
)

// iproc-rng200 registers (relative to SoC base)
const (
	RNG200_OFFSET = 0x1208000

	RNG200_CTRL       = RNG200_OFFSET + 0x00
	RNG200_SOFT_RESET = RNG200_OFFSET + 0x04
	RNG200_RBG_RESET  = RNG200_OFFSET + 0x08
	RNG200_INT_STATUS = RNG200_OFFSET + 0x18
	RNG200_FIFO_DATA  = RNG200_OFFSET + 0x20
	RNG200_FIFO_COUNT = RNG200_OFFSET + 0x24

	RNG200_CTRL_ENABLE     = 0x01
	RNG200_CTRL_MASK       = 0x1FFF
	RNG200_FIFO_COUNT_MASK = 0xFF

	// RNG_INT_STATUS hard-failure bits, as in Linux
	// drivers/char/hw_random/iproc-rng200.c.
	RNG200_INT_MASTER_FAIL_LOCKOUT = 1 << 31
	RNG200_INT_NIST_FAIL           = 1 << 5

	// RNG_INT_STATUS value acknowledging every latched interrupt
	RNG200_INT_STATUS_CLEAR = 0xFFFFFFFF
)

// rngStatusFault reports whether an RNG_INT_STATUS value flags a hard failure
// (MASTER_FAIL_LOCKOUT or NIST_FAIL), after which output cannot be trusted.
func rngStatusFault(status uint32) bool {
	return status&(RNG200_INT_MASTER_FAIL_LOCKOUT|RNG200_INT_NIST_FAIL) != 0
}

// Rng represents a Random number generator instance
type Rng struct {
	sync.Mutex
}

// rngWaitTimeout bounds each reset poll in Init, well above the expected few
// bus round-trips (Linux inserts no delay between reset assert and deassert).
const rngWaitTimeout = 100 * time.Microsecond

// rngWait busy-polls until done returns true or rngWaitTimeout expires,
// reporting whether the condition was observed. It cannot yield as Init runs
// during randinit, before the scheduler is ready; nanotime is already usable
// as socPeripheralBase is statically initialized.
func rngWait(done func() bool) bool {
	for deadline := nanotime() + int64(rngWaitTimeout); nanotime() < deadline; {
		if done() {
			return true
		}
	}

	return false
}

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() {
	r := &Rng{}
	r.Init()

	rng.GetRandomDataFn = r.getRandomData
}

// Init initializes the iproc-rng200 hardware RNG.
func (hw *Rng) Init() {
	hw.Lock()
	defer hw.Unlock()

	hw.reset()
}

// restart acknowledges all latched interrupts and resets the block after a
// health fault, as Linux iproc_rng200_restart() does. The caller must hold hw.
func (hw *Rng) restart() {
	Write32(SoCPeripheralAddress(RNG200_INT_STATUS), RNG200_INT_STATUS_CLEAR)
	hw.reset()
}

// reset runs the iproc-rng200 reset + enable sequence. The caller must hold hw.
func (hw *Rng) reset() {
	// soft reset drains the FIFO, the readback also ensures the assert
	// reached the block before deassert
	Write32(SoCPeripheralAddress(RNG200_SOFT_RESET), 1)
	// timeout intentionally falls through to deassert (see rngWait)
	_ = rngWait(func() bool {
		return Read32(SoCPeripheralAddress(RNG200_FIFO_COUNT))&RNG200_FIFO_COUNT_MASK == 0
	})
	Write32(SoCPeripheralAddress(RNG200_SOFT_RESET), 0)

	// RBG reset, confirmed by the bit reading back as written
	Write32(SoCPeripheralAddress(RNG200_RBG_RESET), 1)
	// timeout intentionally falls through to deassert (see rngWait)
	_ = rngWait(func() bool {
		return Read32(SoCPeripheralAddress(RNG200_RBG_RESET))&1 == 1
	})
	Write32(SoCPeripheralAddress(RNG200_RBG_RESET), 0)

	// enable
	ctrl := Read32(SoCPeripheralAddress(RNG200_CTRL))
	ctrl &= ^uint32(RNG200_CTRL_MASK)
	ctrl |= RNG200_CTRL_ENABLE
	Write32(SoCPeripheralAddress(RNG200_CTRL), ctrl)
}

// fifoCountSpin bounds the wait for a non-empty RNG FIFO, so that a wedged
// entropy source panics on the console instead of silently hanging schedinit.
// A healthy block refills well within it.
const fifoCountSpin = 1 << 20

// waitFIFOCount busy-polls read for a non-zero FIFO count, calling spin
// between polls, and returns the count with whether it was observed within
// fifoCountSpin iterations. It cannot yield as it runs during randinit.
func waitFIFOCount(read func() uint32, spin func()) (count uint32, ok bool) {
	for i := 0; i < fifoCountSpin; i++ {
		if count = read() & RNG200_FIFO_COUNT_MASK; count != 0 {
			return count, true
		}
		spin()
	}

	return 0, false
}

func (hw *Rng) getRandomData(b []byte) {
	hw.Lock()
	defer hw.Unlock()

	read := 0
	restarts := 0

	for read < len(b) {
		// Health check as in Linux iproc_rng200_read(), restarting once
		// on a fault. Being the sole entropy source, a fault surviving
		// the restart panics rather than serving degraded output.
		if rngStatusFault(Read32(SoCPeripheralAddress(RNG200_INT_STATUS))) {
			if restarts++; restarts > 1 {
				panic("bcm2712/rng: TRNG health fault (NIST/master-fail lockout) persists after restart")
			}

			hw.restart()
			continue
		}

		// the FIFO can briefly read empty after the RBG reset
		if _, ok := waitFIFOCount(
			func() uint32 { return Read32(SoCPeripheralAddress(RNG200_FIFO_COUNT)) },
			func() { Busyloop(100) },
		); !ok {
			panic(fmt.Sprintf("bcm2712: RNG FIFO empty after %d polls: entropy source wedged (CTRL %#x, FIFO_COUNT %#x)",
				fifoCountSpin,
				Read32(SoCPeripheralAddress(RNG200_CTRL)),
				Read32(SoCPeripheralAddress(RNG200_FIFO_COUNT))))
		}

		read = rng.Fill(b, read, Read32(SoCPeripheralAddress(RNG200_FIFO_DATA)))
	}
}
