// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package cachemath implements the address arithmetic used by the arm64 data
// cache maintenance routines (see arm64/cache.go and arm64/cache.s).
//
// It has no assembly, unsafe or tamago dependencies, so that arithmetic whose
// only symptom on the target is a coherency side effect can be tested on the
// host.
package cachemath

// LineSize is the data cache line length, in bytes, assumed by the maintenance
// routines. Package arm64 aliases it for the cache.s stride, through go_asm.h,
// and asserts at CPU initialization that the core agrees. It must be a power
// of two.
const LineSize = 64

// DminLineSize returns the minimum data cache line size, in bytes, encoded in
// a CTR_EL0 (Cache Type Register) value. DminLine, bits [19:16], is the log2
// of the smallest line in the system in 32-bit words.
func DminLineSize(ctr uint64) uintptr {
	return 4 << ((ctr >> 16) & 0xf)
}

// LineSpan returns the line aligned base address of the first cache line the
// byte range [start, start+size) touches and the number of consecutive
// LineSize lines it covers, for a caller issuing one maintenance operation per
// line.
//
// The span is derived from the exact range rather than from a rounded up end,
// so a range within a single line spans one line and a range ending on a line
// boundary does not span a further one. An empty range spans none, a zero
// length request must not maintain a line nobody asked for.
func LineSpan(start, size uintptr) (first, n uintptr) {
	if size == 0 {
		return 0, 0
	}

	first = start &^ (LineSize - 1)
	last := (start + size - 1) &^ (LineSize - 1)
	n = (last-first)/LineSize + 1

	return
}
