// BCM2712 SoC support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkramstart

package bcm2712

// Compile-time check of the low-memory scratch map in smp.go. Each constant is
// the gap between a region's end and the next region's start, a negative gap
// (overlap) overflows uint64 and fails the build.
const (
	// mailbox region starts at or after the scratch area start
	_ = uint64(MAILBOX_REGION_BASE - bcm2712ScratchStart)
	// secondary-core stacks start at or after the mailbox region end
	_ = uint64(secondaryStackBase - (MAILBOX_REGION_BASE + MAILBOX_REGION_SIZE))
	// page tables start at or after the secondary-core stacks end
	_ = uint64(pageTablesBase - (secondaryStackBase + MaxCores*secondaryStackSize))
	// the arena holds at least the three fixed tables (L1, L2, L3)
	_ = uint64(pageTablesArena - 0x3000)
	// the guard between the arena and taskBase is at least one page
	_ = uint64(pageTablesGuard - 0x1000)
	// task slots start at or after the page-table region end
	_ = uint64(taskBase - (pageTablesBase + pageTablesSize))
	// task slots end at or below ramStart (bcm2712ScratchEnd)
	_ = uint64(bcm2712ScratchEnd - (taskBase + MaxCores*taskSize))
	// the DMA-coherent window starts at or after the task slots end
	_ = uint64(DMACoherentBase - (taskBase + MaxCores*taskSize))
	// and ends at or below ramStart
	_ = uint64(bcm2712ScratchEnd - (DMACoherentBase + DMACoherentSize))
	// its bounds are 4KB aligned, L3 pages are the finest mapping
	_ = uint64(0 - (DMACoherentBase & 0xfff))
	_ = uint64(0 - (DMACoherentSize & 0xfff))
)
