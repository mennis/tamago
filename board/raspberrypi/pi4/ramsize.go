// Raspberry Pi 4 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pi4

import (
	"runtime"

	"github.com/usbarmory/tamago/soc/bcm2711"
)

// RAMReport describes the RAM region the runtime was linked with next to the
// firmware's view of this board's memory, as returned by [VerifyRAMSize].
//
// The decisive field is SpansCarveOut, and it only means anything when
// CarveOutValid is set: false on a board whose firmware did not answer is
// unknown, not safe.
type RAMReport struct {
	// Start, End and Size are the runtime's RAM region, that is the extent it
	// was linked with rather than how much of it the heap has consumed.
	// Nothing here can measure the latter.
	Start uint64
	End   uint64
	Size  uint64

	// CarveOutStart and CarveOutSize are the carve-out the firmware reports,
	// the memory the ARM must not write, and CarveOutValid reports whether it
	// answered. This is what the type exists for: the boundary is set by
	// gpu_mem and cannot be known at link time, so asking the firmware is the
	// only way to learn where it landed.
	CarveOutStart uint64
	CarveOutSize  uint64
	CarveOutValid bool

	// ARMBankStart and ARMBankSize are the range GET_ARM_MEMORY reports, with
	// ARMBankValid for whether it answered. Supplementary only: the tag's
	// fields are 32-bit and describe the first bank alone, so they cannot
	// express total RAM on a larger board and an exact 4GB bank would wrap to
	// zero and masquerade as an unanswered tag.
	ARMBankStart uint64
	ARMBankSize  uint64
	ARMBankValid bool

	// RevisionRAM is the board's total RAM per the board revision code's RAM
	// size class (bcm2711.BoardRevision, bits [22:20] of a new-style code:
	// 256MB << class), which unlike GET_ARM_MEMORY can express the 4GB and
	// 8GB models. RevisionValid reports whether the code was new-style (bit
	// 23 set) with a known class; when false, RevisionRAM is 0 and the
	// board's total RAM is unknown.
	RevisionRAM   uint64
	RevisionValid bool

	// SpansCarveOut reports whether the linked region reaches into the
	// carve-out where the firmware actually placed it. True is the failure
	// this check exists to catch, and the remedy is a smaller linkramsize
	// region or a smaller gpu_mem. Only meaningful when CarveOutValid is
	// set.
	SpansCarveOut bool

	// Headroom is CarveOutStart - End, the RAM below the carve-out that the
	// linked region does not claim. It is 0 when SpansCarveOut is set (the
	// region has no headroom, it has overlap) and meaningless when
	// CarveOutValid is not.
	//
	// On the stock configuration this is the 4MB margin mem.go's default
	// deliberately leaves; a much larger value means gpu_mem is smaller than
	// the default and a `linkramsize` build could safely claim more.
	Headroom uint64

	// SpansReserved reports whether the linked RAM region overlaps the
	// LINKED carve-out window, [bcm2711.FirmwareRAMStart,
	// bcm2711.FirmwareRAMEnd) — the range arm64's reserved-window hook maps
	// normal cacheable, and which the region is supposed to stop at. It is
	// false for the default build, whose region ends exactly at the window's
	// base. Unlike SpansCarveOut this needs no firmware answer, because both
	// bounds are link-time constants; it checks the build against itself,
	// not against the board.
	SpansReserved bool
}

// VerifyRAMSize reports the RAM region the Go runtime was linked with (see
// mem.go and the `linkramsize` build tag) against the VideoCore firmware's view
// of this board's memory.
//
// The linked size cannot be derived from the firmware at boot (#12): it is
// consumed to place the boot stack and to bound the MMU map before the
// scheduler — and therefore the mailbox — exists. This function is the run-time
// verification half. Applications call it once the board is initialized (from
// or after main, never from early init, since the mailbox property calls rely on
// the scheduler and on DMA cache maintenance) and report the outcome.
//
// On a BCM2711 this is not the formality it is on a Pi 5. The Pi 5's carve-out
// boundary is fixed, so its check can only catch a deliberate `linkramsize`
// override; here the boundary moves with `gpu_mem` in config.txt, so a stock
// build on a board with an unusual boot partition can be wrong through nobody's
// fault. Programs that intend to run on hardware they did not configure should
// treat a set SpansCarveOut as fatal rather than advisory.
//
// The returned report is filled as far as the firmware allowed: err is the
// FIRST mailbox failure encountered, and the fields that query would have
// populated are left zero with their Valid flag clear. A non-nil err therefore
// does not mean the report is empty, and the per-field Valid flags are what a
// caller should branch on.
func VerifyRAMSize() (r RAMReport, err error) {
	r.Start, r.End = runtime.MemRegion()
	r.Size = r.End - r.Start

	r.SpansReserved = r.Start < bcm2711.FirmwareRAMEnd && r.End > bcm2711.FirmwareRAMStart

	if base, size, e := bcm2711.VCMemory(); e != nil {
		err = e
	} else {
		r.CarveOutStart, r.CarveOutSize = uint64(base), uint64(size)
		r.CarveOutValid = true

		// A zero base would mean the firmware answered with a carve-out at
		// address 0, which is bank0's own start and cannot be true; treat
		// it as no answer rather than reporting every region as spanning.
		if r.CarveOutStart == 0 {
			r.CarveOutValid = false
		} else if r.End > r.CarveOutStart {
			r.SpansCarveOut = true
		} else {
			r.Headroom = r.CarveOutStart - r.End
		}
	}

	if base, size, e := bcm2711.ARMMemory(); e != nil {
		if err == nil {
			err = e
		}
	} else {
		r.ARMBankStart, r.ARMBankSize = uint64(base), uint64(size)
		r.ARMBankValid = true
	}

	if rev, e := bcm2711.BoardRevision(); e != nil {
		if err == nil {
			err = e
		}
	} else if rev&(1<<23) != 0 {
		if class := (rev >> 20) & 0x7; class <= 6 {
			r.RevisionRAM = (256 << 20) << class
			r.RevisionValid = true
		}
	}

	return
}
