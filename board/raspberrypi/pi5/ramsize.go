// Raspberry Pi 5 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pi5

import (
	"runtime"

	"github.com/usbarmory/tamago/soc/bcm2712"
)

// RAMReport describes the RAM region the Go runtime was linked with against
// the firmware view of the board memory, as returned by [VerifyRamSize].
type RAMReport struct {
	// Used is the linked RAM size, as reported by runtime.MemRegion.
	Used uint64

	// FWBankStart and FWBankSize are the GET_ARM_MEMORY range
	// (bcm2712.CPUMemory). The tag is 32-bit and describes only the first
	// bank, so it cannot express total RAM above 4GB and an exact 4GB bank
	// wraps to 0. FWBankSize is 0 if the tag was not answered (or wrapped).
	FWBankStart uint64
	FWBankSize  uint64

	// RevisionRAM is the board total RAM from the revision code RAM size
	// class (bits [22:20], 256MB << class). RevisionValid reports whether
	// the code is new-style (bit 23) with a known class, otherwise
	// RevisionRAM is 0.
	RevisionRAM   uint64
	RevisionValid bool

	// SpansFirmwareRAM reports whether the linked region overlaps the
	// firmware carve-out [bcm2712.FirmwareRAMStart, bcm2712.FirmwareRAMEnd),
	// in which case heap growth past its base hands firmware memory to the
	// allocator. It is false for the default build.
	SpansFirmwareRAM bool
}

// VerifyRamSize reports the linked RAM region (see the linkramsize build tag)
// against the board RAM size from the revision code and the GET_ARM_MEMORY
// first bank range.
//
// The linked size cannot be derived from the firmware at boot, this function
// provides the run-time check. It must be called after the scheduler is
// running, as mailbox calls require it.
func VerifyRamSize() (r RAMReport) {
	start, end := runtime.MemRegion()
	r.Used = end - start
	r.SpansFirmwareRAM = start < bcm2712.FirmwareRAMEnd && end > bcm2712.FirmwareRAMStart

	s, n := bcm2712.CPUMemory()
	r.FWBankStart, r.FWBankSize = uint64(s), uint64(n)

	if rev := bcm2712.BoardRevision(); rev&(1<<23) != 0 {
		if class := (rev >> 20) & 0x7; class <= 6 {
			r.RevisionRAM = (256 << 20) << class
			r.RevisionValid = true
		}
	}

	return
}
