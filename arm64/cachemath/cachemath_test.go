// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package cachemath

import "testing"

// TestDminLineDecode is the host-side gate for the line-size assumption: the pure
// CTR_EL0.DminLine decode must map the architected field to the right byte line
// size, so the on-target assert (arm64.assertCacheLineSize) compares against the
// correct value. The A53 and A76 both report DminLine == 4 (64-byte lines).
func TestDminLineDecode(t *testing.T) {
	tests := []struct {
		name string
		ctr  uint64
		want uintptr
	}{
		// Cortex-A53 CTR_EL0 (DDI0500J): DminLine field [19:16] == 4 → 64B.
		{"cortex-a53", 0x84448004, 64},
		// Cortex-A76 CTR_EL0: DminLine field [19:16] == 4 → 64B.
		{"cortex-a76", 0x8444c004, 64},
		// Only bits [19:16] matter; unrelated bits must not perturb the decode.
		{"field 4, other bits set", 0xffff_ffff_fff4_ffff, 64},
		// Smaller and larger lines that must NOT be silently accepted as 64.
		{"field 3 -> 32B", 0x0003_0000, 32},
		{"field 5 -> 128B", 0x0005_0000, 128},
		{"field 0 -> 4B", 0x0000_0000, 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DminLineSize(tc.ctr); got != tc.want {
				t.Fatalf("DminLineSize(%#x) = %d; want %d", tc.ctr, got, tc.want)
			}
		})
	}
}

// TestLineSpan is the host-side gate for the range translation: the by-VA cache
// range math must never touch a line the caller did not ask for.
func TestLineSpan(t *testing.T) {
	tests := []struct {
		name        string
		start, size uintptr
		wantFirst   uintptr
		wantN       uintptr
	}{
		// size == 0 must touch NO lines, whatever the start alignment.
		// Before the fix the asm loop was do-while and discarded one line.
		{"zero size, unaligned start", 0x1001, 0, 0, 0},
		{"zero size, aligned start", 0x1000, 0, 0, 0},

		// A sub-line range covers exactly the one line that holds it.
		{"one byte just past a boundary", 0x1001, 1, 0x1000, 1},
		{"sub-line range mid-line", 0x1010, 0x10, 0x1000, 1},

		// Already line-aligned inputs are unchanged: first == start, and an
		// exact-multiple size does not round an extra (unrequested) line in.
		{"aligned start and size, one line", 0x1000, 0x40, 0x1000, 1},
		{"aligned start and size, two lines", 0x1000, 0x80, 0x1000, 2},

		// One byte past a line boundary reaches into, but no further than, the
		// next line. Verbatim from the issue's acceptance test.
		{"one byte over into the next line", 0xfc0, 0x41, 0xfc0, 2},

		// Unaligned start whose range crosses one boundary spans two lines and
		// stops there — no rounding past the requested end.
		{"unaligned start across one boundary", 0x1030, 0x20, 0x1000, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, n := LineSpan(tc.start, tc.size)
			if first != tc.wantFirst || n != tc.wantN {
				t.Fatalf("LineSpan(%#x, %#x) = (first=%#x, n=%d); want (first=%#x, n=%d)",
					tc.start, tc.size, first, n, tc.wantFirst, tc.wantN)
			}
		})
	}
}

// TestLineSpanNoRoundingPastEnd asserts the invariant that keeps invalidate from
// discarding a line beyond the requested range: the last counted line is the one
// holding the final requested byte, and no line is counted past it.
func TestLineSpanNoRoundingPastEnd(t *testing.T) {
	for _, size := range []uintptr{1, 0x20, 0x3f, 0x40, 0x41, 0x1000} {
		for _, start := range []uintptr{0, 1, 0x20, 0x1000, 0x1001, 0x1fc0} {
			first, n := LineSpan(start, size)

			end := start + size
			lastByte := end - 1
			wantLastLine := lastByte &^ (LineSize - 1)
			lastLine := first + (n-1)*LineSize

			if first > start {
				t.Errorf("LineSpan(%#x, %#x): first %#x is above start", start, size, first)
			}
			if lastLine != wantLastLine {
				t.Errorf("LineSpan(%#x, %#x): last line %#x, want %#x (the line holding start+size-1)",
					start, size, lastLine, wantLastLine)
			}
			// The line after the last counted one must lie at or beyond the end,
			// i.e. we never reach into a line the range does not touch.
			if firstUncovered := first + n*LineSize; firstUncovered < end {
				t.Errorf("LineSpan(%#x, %#x): stops at %#x, before end %#x", start, size, firstUncovered, end)
			}
		}
	}
}

// TestInvalidateZeroSizeNoop is the issue's zero-size no-op gate. A cache.go
// wrapper only invokes the assembly when n != 0, so proving n == 0 for every
// zero-size request proves zero assembly invocations — no line is invalidated,
// cleaned, or flushed.
func TestInvalidateZeroSizeNoop(t *testing.T) {
	for _, start := range []uintptr{0, 1, 0x1000, 0x1001, 0xfff, 0xdeadbeef} {
		if _, n := LineSpan(start, 0); n != 0 {
			t.Errorf("LineSpan(%#x, 0) = n %d; want 0 (a zero-length request must issue no maintenance)", start, n)
		}
	}
}
