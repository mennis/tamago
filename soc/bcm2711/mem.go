// BCM2711 SoC memory layout
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	_ "unsafe"
)

// The low memory scratch window holds the translation tables, which cannot go
// where arm64 places them by default: the firmware loads the image at
// kernel_address, 0x80000, so ramStart + 0x4000 lands on this image's own text.
// Everything below the image is free once the firmware has jumped to us.
//
// ScratchStart is not zero because the first page holds the firmware armstub
// and the spin table cores 1 to 3 park on. Anyone adding SMP here should note
// that stock firmware offers no PSCI, so a release word lives inside the page
// mapped invalid as the null pointer trap.
const (
	ScratchStart = 0x00001000
	ScratchEnd   = 0x00080000

	// PageTablesEnd bounds the page table arena that starts at ScratchStart:
	// eight pages, three fixed tables and room for five refinements, where one
	// is the most the constants below can demand.
	//
	// It is bounded at all, rather than left to run to the image, so that the
	// next tenant added down here collides with an allocator panic rather than
	// with a live page table. arm64/mmucheck's TestArenaFitsReservations
	// derives the demand from these bounds and is where an edit to them is
	// checked.
	PageTablesEnd = ScratchStart + 0x8000
)

//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x00080000

//go:linkname lowMemStart github.com/usbarmory/tamago/arm64.lowMemStart
var lowMemStart uint64 = ScratchStart

//go:linkname lowMemEnd github.com/usbarmory/tamago/arm64.lowMemEnd
var lowMemEnd uint64 = ScratchEnd

//go:linkname pageTableStart github.com/usbarmory/tamago/arm64.pageTableStart
var pageTableStart uint64 = ScratchStart

//go:linkname pageTableLimit github.com/usbarmory/tamago/arm64.pageTableLimit
var pageTableLimit uint64 = PageTablesEnd

// RAM on this SoC is not contiguous. The firmware carves a region out of the
// low gigabyte for the VideoCore and grants framebuffers inside it, and the
// size of that carve-out is set by gpu_mem in config.txt, so no single address
// is correct for every board: a larger gpu_mem moves its base down.
//
// The constants below are therefore a conservative default rather than a
// measurement that holds everywhere. A board raising gpu_mem past it must lower
// its own RamSize to match, see VerifyRAMSize.
const (
	// FirmwareRAMStart is where the normal cacheable window begins, well below
	// where the carve-out starts on a stock board.
	//
	// The window and the RAM region have to move together and only one of them
	// can, as RamSize is overridable through the linkramsize build tag while
	// these linknames are not. Anything left between a lowered RAM region and
	// the window falls through to the default Device mapping, where the
	// byte-width stores a framebuffer consumer makes will fault. Starting low
	// costs the default build nothing, as the RAM region is classified first.
	FirmwareRAMStart = 0x20000000

	// FirmwareRAMEnd is the top of the low gigabyte, where the GPU alias ends.
	FirmwareRAMEnd = 0x40000000

	// CarveOutDefault is where the carve-out starts on a stock gpu_mem,
	// measured on a Pi 4 Model B Rev 1.5. Unlike FirmwareRAMStart, which is a
	// mapping boundary chosen with margin, this is an observation.
	CarveOutDefault = 0x3b400000

	// RAMSafeEnd is the highest address a board should let the runtime's region
	// reach, 4 MB below the carve-out so that a modestly larger gpu_mem still
	// lands above the heap rather than inside it. Board packages size RamSize
	// against this rather than against FirmwareRAMStart.
	RAMSafeEnd = CarveOutDefault - 0x400000
)

// The carve-out is mapped normal cacheable and execute never, outside the
// runtime's RAM region. Outside, so that the heap can never be handed
// firmware-owned memory; cacheable, so that a framebuffer in it can be drawn
// with ordinary stores rather than faulting on Device memory alignment rules.

//go:linkname reservedMemStart github.com/usbarmory/tamago/arm64.reservedMemStart
var reservedMemStart uint64 = FirmwareRAMStart

//go:linkname reservedMemEnd github.com/usbarmory/tamago/arm64.reservedMemEnd
var reservedMemEnd uint64 = FirmwareRAMEnd
