// BCM2712 SoC support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkramstart

package bcm2712

import (
	_ "unsafe"
)

// Low-memory scratch area, its layout is the scratch map in smp.go, from which
// the page table base is taken.
const (
	bcm2712ScratchStart = 0x00001000
	bcm2712ScratchEnd   = 0x00080000
	bcm2712PageTables   = pageTablesBase
)

// DMACoherentBase and DMACoherentSize define the DMA-coherent window, mapped
// Normal Non-cacheable by the arm64 MMU. Bus masters sharing packed structures
// with the CPU (e.g. 16-byte GEM descriptors, four per cache line) must use it,
// as line-granular cache maintenance would overwrite neighbouring entries.
//
// The window lies below ramStart, outside the heap and stack, and RP1 reaches
// it through the PCIe inbound window (bus address 0x10_00000000 + physical).
const (
	DMACoherentBase = 0x00030000
	DMACoherentSize = 0x00040000 // 256KB: rings + 1536-byte frame buffers
)

//go:linkname dmaMemStart github.com/usbarmory/tamago/arm64.dmaMemStart
var dmaMemStart uint64 = DMACoherentBase

//go:linkname dmaMemEnd github.com/usbarmory/tamago/arm64.dmaMemEnd
var dmaMemEnd uint64 = DMACoherentBase + DMACoherentSize

//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x00080000

//go:linkname lowMemStart github.com/usbarmory/tamago/arm64.lowMemStart
var lowMemStart uint64 = bcm2712ScratchStart

//go:linkname lowMemEnd github.com/usbarmory/tamago/arm64.lowMemEnd
var lowMemEnd uint64 = bcm2712ScratchEnd

//go:linkname pageTableStart github.com/usbarmory/tamago/arm64.pageTableStart
var pageTableStart uint64 = bcm2712PageTables

// pageTableLimit bounds the page table arena, it excludes the guard page so
// that the arena can never grow into the SMP task slots.
//
//go:linkname pageTableLimit github.com/usbarmory/tamago/arm64.pageTableLimit
var pageTableLimit uint64 = bcm2712PageTables + pageTablesArena

// The VideoCore firmware owns the top of the first RAM bank, from 0x3f800000
// up to 0x40000000 where RAM resumes; its framebuffer moves down to 0x3f400000
// with a 32bpp config.txt.
//
// FirmwareRAMStart to FirmwareRAMEnd is mapped normal cacheable, execute-never,
// for framebuffer access but must stay outside runtime/goos.RamSize. The window
// starts 4MB below the lowest observed allocation and must be 2MB aligned, as
// the MMU maps it with 2MB blocks.
const (
	FirmwareRAMStart = 0x3f000000
	FirmwareRAMEnd   = 0x40000000
)

//go:linkname reservedMemStart github.com/usbarmory/tamago/arm64.reservedMemStart
var reservedMemStart uint64 = FirmwareRAMStart

//go:linkname reservedMemEnd github.com/usbarmory/tamago/arm64.reservedMemEnd
var reservedMemEnd uint64 = FirmwareRAMEnd
