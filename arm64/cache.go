// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

import (
	"github.com/usbarmory/tamago/arm64/cachemath"
)

// defined in cache.s
//
// The range operations take the line aligned address of the first cache line
// and the number of consecutive lines to maintain, the address and size to
// line span translation lives in cachemath so that it can be tested on the
// host.
func cache_enable()
func cache_disable()
func clean_data_cache_range(first, n uintptr)
func invalidate_data_cache_range(first, n uintptr)
func flush_data_cache_range(first, n uintptr)
func data_synchronization_barrier()
func read_ctr_el0() uint64

// cacheLineSize is the data cache line length assumed by the maintenance
// loops, cache.s reads it through go_asm.h.
const cacheLineSize = cachemath.LineSize

// assertCacheLineSize panics unless the calling core reports cacheLineSize as
// its architected minimum data cache line size. The maintenance loops step by
// a fixed stride, on a core with a smaller line they would skip every
// intervening line and silently corrupt DMA buffers rather than fault.
func assertCacheLineSize() {
	if cachemath.DminLineSize(read_ctr_el0()) != cacheLineSize {
		panic("unsupported data cache line size")
	}
}

// EnableCache activates the ARM instruction and data caches.
func (cpu *CPU) EnableCache() {
	cache_enable()
}

// DisableCache disables the ARM instruction and data caches.
func (cpu *CPU) DisableCache() {
	cache_disable()
}

// FlushTLBs flushes the ARM Translation Lookaside Buffers.
func (cpu *CPU) FlushTLBs() {
	flush_tlb()
}

// CleanDataCacheRange writes back every dirty cache line overlapping the byte
// range [start, start+size). A partially covered line is written back in full,
// which is harmless. A zero size touches no line.
func (cpu *CPU) CleanDataCacheRange(start, size uintptr) {
	if first, n := cachemath.LineSpan(start, size); n != 0 {
		clean_data_cache_range(first, n)
	}
}

// InvalidateDataCacheRange discards every cache line overlapping the byte
// range [start, start+size) without writing it back, so that a subsequent read
// observes memory (e.g. a buffer a device just filled). A zero size touches no
// line.
//
// A partially covered leading or trailing line is discarded in full and any
// dirty data it holds elsewhere is lost. Callers must therefore either pass a
// line aligned range or own the whole of the lines it overlaps.
//
// This is deliberately never a clean and invalidate, as a descriptor ring
// relies on invalidation not writing a stale line back over a neighbouring
// descriptor the hardware owns.
func (cpu *CPU) InvalidateDataCacheRange(start, size uintptr) {
	if first, n := cachemath.LineSpan(start, size); n != 0 {
		invalidate_data_cache_range(first, n)
	}
}

// FlushDataCacheRange writes back and then discards every cache line
// overlapping the byte range [start, start+size). A zero size touches no line.
func (cpu *CPU) FlushDataCacheRange(start, size uintptr) {
	if first, n := cachemath.LineSpan(start, size); n != 0 {
		flush_data_cache_range(first, n)
	}
}

// DataSynchronizationBarrier executes a full system data synchronization
// barrier, every memory access before it is globally observed by the time it
// returns. Use it to order device register writes against each other or before
// handing a buffer to a bus mastering device.
func (cpu *CPU) DataSynchronizationBarrier() {
	data_synchronization_barrier()
}
