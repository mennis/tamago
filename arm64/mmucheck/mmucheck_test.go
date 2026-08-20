// ARM64 MMU window-alignment checks
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package mmucheck

import "testing"

// A real board's live layout, as one concrete case among the synthetic ones.
const (
	liveDMAStart = 0x30000
	liveDMAEnd   = 0x70000
	liveResStart = 0x3f000000
	liveResEnd   = 0x40000000
	liveLowStart = 0x1000
	liveLowEnd   = 0x80000
)

func TestWindowAlignment(t *testing.T) {
	tests := []struct {
		name                                                 string
		dmaStart, dmaEnd, resStart, resEnd, lowStart, lowEnd uint64
		wantErr                                              bool
	}{
		{
			name:     "live board layout",
			dmaStart: liveDMAStart, dmaEnd: liveDMAEnd,
			resStart: liveResStart, resEnd: liveResEnd,
			lowStart: liveLowStart, lowEnd: liveLowEnd,
			wantErr: false,
		},
		{
			name:    "all windows disabled",
			wantErr: false,
		},
		{
			name:     "dma window not page aligned at the start",
			dmaStart: 0x30800, dmaEnd: 0x70000,
			wantErr: true,
		},
		{
			name:     "dma window not page aligned at the end",
			dmaStart: 0x30000, dmaEnd: 0x70800,
			wantErr: true,
		},
		{
			name:     "reserved window not page aligned",
			resStart: 0x3f000800, resEnd: 0x40000000,
			wantErr: true,
		},
		{
			name:     "low window not page aligned",
			lowStart: 0x1001, lowEnd: 0x80000,
			wantErr: true,
		},
		{
			name:     "dma window inverted",
			dmaStart: 0x70000, dmaEnd: 0x30000,
			wantErr: true,
		},
		{
			// A non-zero end means the window is enabled, so an empty range is
			// a declaration mistake and is named. Only end == 0 disables.
			name:     "dma window empty but enabled",
			dmaStart: 0x30000, dmaEnd: 0x30000,
			wantErr: true,
		},

		// The cases below were REJECTED before arm64 could refine a partially
		// covered block, and are accepted now. They are here so a regression to
		// the old, stricter rules fails a test rather than silently narrowing
		// what a platform may declare. See the package doc.
		{
			name:     "page-aligned dma window outside any 2MB boundary",
			dmaStart: 0x40001000, dmaEnd: 0x40003000,
			wantErr: false,
		},
		{
			name:     "reserved window aligned to 4KB but not 2MB",
			resStart: 0x3f001000, resEnd: 0x3f002000,
			wantErr: false,
		},
		{
			name:     "low-memory window extending past the first 2MB",
			lowStart: 0x1000, lowEnd: 0x400000,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWindows(
				tc.dmaStart, tc.dmaEnd,
				tc.resStart, tc.resEnd,
				tc.lowStart, tc.lowEnd,
			)

			if got := err != nil; got != tc.wantErr {
				t.Errorf("ValidateWindows() error = %v, want error = %v", err, tc.wantErr)
			}
		})
	}
}

func TestIsBlockAligned(t *testing.T) {
	tests := []struct {
		x, block uint64
		want     bool
	}{
		{0, block4KB, true},
		{block4KB, block4KB, true},
		{block4KB - 1, block4KB, false},
		{block4KB + 1, block4KB, false},
		{0x30000, block4KB, true},
		{0x30800, block4KB, false},
	}

	for _, tc := range tests {
		if got := IsBlockAligned(tc.x, tc.block); got != tc.want {
			t.Errorf("IsBlockAligned(%#x, %#x) = %v, want %v", tc.x, tc.block, got, tc.want)
		}
	}
}

// TestArenaCountsSplitSites pins the counting rule itself.
func TestArenaCountsSplitSites(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    Layout
		want int
	}{
		{
			name: "everything aligned costs nothing",
			l: Layout{
				RAMStart: 0x40000000, TextEnd: 0x40000000, RAMEnd: 0x80000000,
			},
			want: 0,
		},
		{
			name: "first 2MB is free, being the fixed L3",
			l: Layout{
				RAMStart: 0x1000, TextEnd: 0x2000, RAMEnd: 0x80000,
			},
			want: 0,
		},
		{
			name: "a window past 1GB splits at both levels",
			l: Layout{
				RAMStart: 0x40000000, TextEnd: 0x40000000, RAMEnd: 0x80000000,
				DMAStart: 0x40001000, DMAEnd: 0x40002000,
			},
			want: 2, // one L2 for the 1GB block, one L3 for the 2MB block
		},
		{
			name: "two boundaries in one block share its table",
			l: Layout{
				RAMStart: 0x40000000, TextEnd: 0x40001000, RAMEnd: 0x80000000,
				DMAStart: 0x40002000, DMAEnd: 0x40003000,
			},
			want: 2,
		},
		{
			name: "distinct blocks do not",
			l: Layout{
				RAMStart: 0x40000000, TextEnd: 0x40000000, RAMEnd: 0x80000000,
				DMAStart: 0x40001000, DMAEnd: 0x40001000 + block2MB,
			},
			want: 3, // one L2, and an L3 for each of the two 2MB blocks
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.ArenaTables(); got != tc.want {
				t.Errorf("ArenaTables() = %d, want %d", got, tc.want)
			}
		})
	}
}
