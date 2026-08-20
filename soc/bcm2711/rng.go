// BCM2711 SoC Random Number Generator (RNG) driver
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"sync"
	"time"
	_ "unsafe"

	"github.com/usbarmory/tamago/internal/rng"
)

// hex32 renders v as a hex string without fmt, which its one caller cannot use:
// the panic in getRandomData runs during randinit, before procresize has given
// this m a P, and fmt.Sprintf reaches sync.Pool, which pins to a P and
// dereferences nil without one. The panic naming a wedged entropy source would
// otherwise fault inside its own argument list.
func hex32(v uint32) string {
	const digits = "0123456789abcdef"

	b := [10]byte{'0', 'x'}

	for i := 0; i < 8; i++ {
		b[2+i] = digits[(v>>uint(28-4*i))&0xf]
	}

	return string(b[:])
}

// iproc-rng200 registers (brcm,bcm2711-rng200), relative to RNGOffset.
const (
	RNGCtrl      = RNGOffset + 0x00
	RNGSoftReset = RNGOffset + 0x04
	RNGRBGReset  = RNGOffset + 0x08
	RNGIntStatus = RNGOffset + 0x18
	RNGFIFOData  = RNGOffset + 0x20
	RNGFIFOCount = RNGOffset + 0x24

	RNGCtrlEnable    = 0x01
	RNGCtrlMask      = 0x1FFF
	RNGFIFOCountMask = 0xFF

	// RNGIntStatus health bits, the two hard failures iproc_rng200_read()
	// checks before trusting FIFO output and restarts the block on.
	RNGIntMasterFailLockout = 1 << 31
	RNGIntNISTFail          = 1 << 5

	// RNGIntStatusClear is the clear-all value iproc_rng200_restart()
	// writes to INT_STATUS to acknowledge every latched interrupt before
	// resetting.
	RNGIntStatusClear = 0xFFFFFFFF
)

// rngStatusFault reports whether an INT_STATUS value says the ring
// oscillator's output can no longer be trusted. It performs no MMIO, so the
// decision can be exercised apart from the register accessors.
func rngStatusFault(status uint32) bool {
	return status&(RNGIntMasterFailLockout|RNGIntNISTFail) != 0
}

// Rng represents a Random number generator instance.
type Rng struct {
	sync.Mutex
}

// rngWaitTimeout bounds each reset-observable poll in Init. 100µs is orders
// of magnitude above the few-bus-round-trip settle seen on real iproc-rng200
// silicon (the Linux driver inserts no delay at all between reset assert and
// deassert).
const rngWaitTimeout = 100 * time.Microsecond

// rngWait busy-polls until done reports true or the timeout expires,
// returning whether the condition was observed. It must spin rather than
// yield, as Init is wired to runtime/goos.InitRNG and runs during randinit
// before the scheduler is ready. On timeout it simply returns, so unexpected
// hardware state degrades to a bounded settle rather than hanging boot.
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

// restart re-initializes the block after a health fault, mirroring the
// Linux iproc_rng200_restart() sequence: acknowledge every latched
// interrupt by writing 0xFFFFFFFF to INT_STATUS, then re-run the
// reset+enable. The caller must already hold hw (getRandomData restarts
// while locked).
func (hw *Rng) restart() {
	Write32(PeripheralAddress(RNGIntStatus), RNGIntStatusClear)
	hw.reset()
}

// reset runs the iproc-rng200 reset + enable sequence. The caller must hold
// hw.
func (hw *Rng) reset() {
	// soft reset: if the FIFO holds data at entry (re-init, firmware
	// residue), holding SOFT_RESET drains it and the poll observes that;
	// on a cold boot the FIFO is already empty and the readback simply
	// forces the reset write to post at the block (Device-nGnRnE ordering)
	// before deassert - Linux iproc-rng200 inserts no delay here.
	Write32(PeripheralAddress(RNGSoftReset), 1)
	// timeout intentionally falls through to deassert (see rngWait)
	_ = rngWait(func() bool {
		return Read32(PeripheralAddress(RNGFIFOCount))&RNGFIFOCountMask == 0
	})
	Write32(PeripheralAddress(RNGSoftReset), 0)

	// RBG reset. The bit generator exposes no FIFO visible reset state, so
	// the observable is the reset bit reading back as written. This exists
	// because the IP has it, not because a fault was seen without it:
	// resetting the RNG alone leaves the entropy source running across a
	// re-init.
	Write32(PeripheralAddress(RNGRBGReset), 1)
	// timeout intentionally falls through to deassert (see rngWait)
	_ = rngWait(func() bool {
		return Read32(PeripheralAddress(RNGRBGReset))&1 == 1
	})
	Write32(PeripheralAddress(RNGRBGReset), 0)

	// enable
	ctrl := Read32(PeripheralAddress(RNGCtrl))
	ctrl &= ^uint32(RNGCtrlMask)
	ctrl |= RNGCtrlEnable
	Write32(PeripheralAddress(RNGCtrl), ctrl)
}

// fifoCountSpin bounds the busy-wait for the FIFO to become non-empty, turning
// a silent livelock into a named panic: getRandomData runs during randinit,
// before the scheduler and before any console indication of progress, so an
// unclocked or held entropy source would otherwise hang schedinit with no
// output. The bound is far above the post-reset refill latency seen on
// silicon, so a healthy block never trips it.
//
// Nothing on this path may call runtime.Gosched or otherwise depend on a
// running scheduler.
const fifoCountSpin = 1 << 20

// waitFIFOCount busy-polls read for a non-zero FIFO count, spacing polls with
// spin to give the ring oscillators time, and returns the count and whether the
// FIFO filled within the bound. It performs no MMIO of its own, so the bound
// can be exercised in isolation.
func waitFIFOCount(read func() uint32, spin func()) (count uint32, ok bool) {
	for i := 0; i < fifoCountSpin; i++ {
		if count = read() & RNGFIFOCountMask; count != 0 {
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
		// Health check. A latched NIST_FAIL or MASTER_FAIL_LOCKOUT
		// means the FIFO must not be drained; restart the block once
		// and re-check. Being the sole crypto/rand entropy source this
		// then fails closed rather than serving degraded entropy.
		if rngStatusFault(Read32(PeripheralAddress(RNGIntStatus))) {
			if restarts++; restarts > 1 {
				panic("bcm2711/rng: TRNG health fault (NIST/master-fail lockout) persists after restart")
			}

			hw.restart()
			continue
		}

		// The FIFO can read empty briefly after reset, so wait for it
		// to refill; one that never does trips the bound and panics
		// with the CTRL and FIFO_COUNT state.
		if _, ok := waitFIFOCount(
			func() uint32 { return Read32(PeripheralAddress(RNGFIFOCount)) },
			func() { Busyloop(100) },
		); !ok {
			panic("bcm2711: RNG FIFO empty after 2^20 polls: entropy source wedged (CTRL " +
				hex32(Read32(PeripheralAddress(RNGCtrl))) + ", FIFO_COUNT " +
				hex32(Read32(PeripheralAddress(RNGFIFOCount))) + ")")
		}

		read = rng.Fill(b, read, Read32(PeripheralAddress(RNGFIFOData)))
	}
}
