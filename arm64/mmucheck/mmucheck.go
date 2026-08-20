// ARM64 MMU window-alignment checks
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package mmucheck implements the alignment and page table budget checks for
// the optional memory windows arm64.InitMMU maps.
//
// It has no tamago dependencies, so that a platform can assert its own memory
// map on the host rather than on the board, where an exhausted page table
// reservation arrives before there is a console to report it.
//
// The table walk maps 1GB blocks at L1, 2MB blocks at L2 and 4KB pages at L3,
// refining a partially covered block to the next level down. A window may
// therefore be aligned to anything down to a page, but no finer, as L3 has
// nothing to split into and the walk would panic mid build without naming the
// window at fault.
package mmucheck

import "fmt"

const (
	block4KB uint64 = 1 << 12 // L3 page, the finest mapping the walk expresses
	block2MB uint64 = 1 << 21 // L2 block
	block1GB uint64 = 1 << 30 // L1 block
)

// IsBlockAligned reports whether x is a multiple of the given block size.
// block must be a power of two.
func IsBlockAligned(x, block uint64) bool {
	return x&(block-1) == 0
}

// ValidateWindows checks that the optional DMA-coherent, reserved and low
// memory windows can be expressed by the table walk, every bound must fall on
// a page boundary and no window may be empty or inverted. A window whose end
// bound is zero is disabled and skipped.
func ValidateWindows(dmaStart, dmaEnd, resStart, resEnd, lowStart, lowEnd uint64) error {
	for _, w := range []struct {
		name       string
		start, end uint64
		why        string
	}{
		{"dma", dmaStart, dmaEnd, "a coherent window that cannot be mapped exactly would be mapped cacheable, which is what it exists to avoid"},
		{"reserved", resStart, resEnd, "a reserved window that cannot be mapped exactly would be mapped as Device, whose alignment rules fault ordinary stores"},
		{"low-memory", lowStart, lowEnd, "a scratch window that cannot be mapped exactly would be mapped as Device"},
	} {
		if w.end == 0 {
			continue
		}

		if w.start >= w.end {
			return fmt.Errorf("%s window empty or inverted: [%#x, %#x)", w.name, w.start, w.end)
		}

		if !IsBlockAligned(w.start, block4KB) || !IsBlockAligned(w.end, block4KB) {
			return fmt.Errorf("%s window [%#x, %#x) is not 4KB-aligned; %s", w.name, w.start, w.end, w.why)
		}
	}

	return nil
}

// ValidatePageTables checks that the DMA-coherent window does not cover the
// page tables at [base, arenaEnd).
//
// The walker fetches descriptors normal cacheable, so a coherent window over
// them would leave the CPU writing descriptors through one attribute while the
// walker reads them through another. The low memory and reserved windows are
// themselves normal cacheable and agree with the walker, so a platform may
// place its tables inside one: that is the usual arrangement, since the scratch
// space below the image is exactly where the tables go when firmware loads at
// ramStart.
//
// A zero dmaEnd means no coherent window is configured.
func ValidatePageTables(dmaStart, dmaEnd, base, arenaEnd uint64) error {
	if dmaEnd == 0 {
		return nil
	}

	if dmaStart < arenaEnd && dmaEnd > base {
		return fmt.Errorf("DMA-coherent window [%#x, %#x) overlaps the page tables [%#x, %#x)",
			dmaStart, dmaEnd, base, arenaEnd)
	}

	return nil
}

// Layout is the set of address-space boundaries arm64.InitMMU's walk sees. A
// window whose End is zero is disabled and contributes nothing.
type Layout struct {
	RAMStart, TextEnd, RAMEnd  uint64
	DMAStart, DMAEnd           uint64
	ReservedStart, ReservedEnd uint64
	LowStart, LowEnd           uint64
}

// ArenaTables reports how many page tables arm64.InitMMU allocates from its
// arena for this layout, over and above the three fixed tables, so that a
// platform can assert the count against its own reservation.
//
// A table is allocated exactly when a boundary between differently mapped
// regions falls strictly inside a block, so the count is the number of distinct
// blocks holding at least one interior boundary, summed over L1 and L2. Block 0
// is free at both levels, InitMMU pre-splits the first 1GB and the first 2MB
// before the walk begins.
//
// Counting split sites rather than replaying the attribute decisions cannot
// drift as those change, it can only overcount by assuming both sides of a
// boundary differ.
func (l Layout) ArenaTables() int {
	var bounds []uint64

	for _, w := range [][2]uint64{
		{l.RAMStart, l.TextEnd}, {l.TextEnd, l.RAMEnd},
		{l.DMAStart, l.DMAEnd},
		{l.ReservedStart, l.ReservedEnd},
		{l.LowStart, l.LowEnd},
	} {
		if w[1] == 0 {
			continue
		}

		bounds = append(bounds, w[0], w[1])
	}

	tables := 0

	for _, block := range [...]uint64{block1GB, block2MB} {
		seen := make(map[uint64]bool)

		for _, b := range bounds {
			if IsBlockAligned(b, block) {
				// on an edge: splits nothing
				continue
			}

			if index := b / block; index != 0 && !seen[index] {
				seen[index] = true
				tables++
			}
		}
	}

	return tables
}

// ArenaEnd is the first address the page table allocator may not cross.
//
// A platform states it as pageTableLimit. That is optional: requiring it would
// fail a board that relocates its tables and says nothing more, and the failure
// is unreportable, as InitMMU runs before the exception vectors are installed.
//
// Unstated, tables inside the low memory window are bounded by its end, since
// they may not leave the region reserved for them. Otherwise textStart, which
// is what the default placement uses.
func ArenaEnd(base, limit, lowStart, lowEnd, textStart uint64) uint64 {
	if limit != 0 {
		return limit
	}

	if lowEnd != 0 && base >= lowStart && base < lowEnd {
		return lowEnd
	}

	return textStart
}
