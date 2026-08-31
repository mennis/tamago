// ARM64 MMU window checks
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package mmucheck

import (
	"strings"
	"testing"
)

// TestArenaFitsReservations checks each platform's layout against the arena it
// publishes to arm64 as pageTableLimit.
//
// The bounds are restated rather than imported, as a SoC package cannot be
// linked into a host test, which is the same constraint that put this package
// in a leaf of its own. A layout edit that outgrows its reservation fails here
// rather than as an allocator panic on a board with no console yet.
func TestArenaFitsReservations(t *testing.T) {
	// worst case, an image whose end does not fall on a block boundary
	const textEnd = 0x2b4000

	for _, tc := range []struct {
		name string
		l    Layout
		// arena pages reserved beyond the three fixed tables
		budget int
	}{
		{
			// soc/bcm2711/mem.go: PageTablesEnd is ScratchStart + 0x8000,
			// eight pages, three fixed and five for the arena.
			name: "bcm2711",
			l: Layout{
				RAMStart: 0x80000, TextEnd: textEnd, RAMEnd: 0x3b000000,
				ReservedStart: 0x20000000, ReservedEnd: 0x40000000,
				LowStart: 0x1000, LowEnd: 0x80000,
			},
			budget: 5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWindows(tc.l.DMAStart, tc.l.DMAEnd,
				tc.l.ReservedStart, tc.l.ReservedEnd,
				tc.l.LowStart, tc.l.LowEnd); err != nil {
				t.Fatalf("windows rejected: %v", err)
			}

			got := tc.l.ArenaTables()

			if got > tc.budget {
				t.Errorf("needs %d arena tables, the reservation holds %d", got, tc.budget)
			}

			// Pinned because each SoC derives its reservation from this
			// number by hand.
			if got != 1 {
				t.Errorf("arena demand %d, want 1", got)
			}
		})
	}
}

// TestPageTablesInsideLowMemory pins the case that a real platform actually
// uses and that an over-broad version of this check rejected: the tables live
// inside the low memory scratch window, which is normal cacheable and so
// agrees with the walker.
//
// It is here rather than in arm64 because arm64 cannot be built off-target,
// which is exactly how the over-broad check reached hardware -- it panicked in
// Hwinit0, before any console existed, and nothing in the suite could see it.
func TestPageTablesInsideLowMemory(t *testing.T) {
	// soc/bcm2711: scratch [0x1000, 0x80000), tables [0x1000, 0x9000)
	const base, arenaEnd = 0x1000, 0x9000

	if err := ValidatePageTables(0, 0, base, arenaEnd); err != nil {
		t.Errorf("tables inside the scratch window rejected with no DMA window: %v", err)
	}

	// a coherent window that really does overlap must be refused
	if err := ValidatePageTables(0x2000, 0x5000, base, arenaEnd); err == nil {
		t.Error("a coherent window overlapping the tables was accepted")
	}

	// soc/bcm2711 configures no coherent window; soc/bcm2712's is [0x30000,
	// 0x70000), clear of tables at [0x1000, 0x9000)
	if err := ValidatePageTables(0x30000, 0x70000, base, arenaEnd); err != nil {
		t.Errorf("a coherent window clear of the tables rejected: %v", err)
	}
}

// TestArenaEnd covers a relocated arena, the case no board here has: tables
// below ramStart in a low memory window, which is what a platform whose
// firmware loads the image at ramStart must do.
func TestArenaEnd(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		base, limit, lowS, lowE, text uint64
		want                          uint64
	}{
		{"a stated limit wins", 0x1000, 0x9000, 0x1000, 0x80000, 0x80000, 0x9000},
		{"unstated, inside the low window", 0x1000, 0, 0x1000, 0x80000, 0x80000, 0x80000},
		// lowEnd and textStart coincide above, so that case cannot tell the
		// two branches apart. Here they differ and the window's end wins.
		{"unstated, window ends below the image", 0x1000, 0, 0x1000, 0x40000, 0x80000, 0x40000},
		{"unstated, no low window", 0x40004000, 0, 0, 0, 0x40100000, 0x40100000},
		{"unstated, tables above the window", 0x90000, 0, 0x1000, 0x80000, 0x100000, 0x100000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ArenaEnd(tc.base, tc.limit, tc.lowS, tc.lowE, tc.text)

			if got != tc.want {
				t.Errorf("ArenaEnd() = %#x, want %#x", got, tc.want)
			}

			if got <= tc.base {
				t.Errorf("bound %#x leaves no arena above base %#x", got, tc.base)
			}
		})
	}
}

// TestValidateLayout covers the conditions that were bare panics inside
// InitMMU. Each is reachable by an ordinary platform-configuration mistake and
// each aborts before the exception vectors are installed, so the board that
// hits one produces no output at all.
//
// The accepted case is the shape with the tables relocated below an image
// linked at ramStart, as that is the layout the panics were reachable from.
func TestValidateLayout(t *testing.T) {
	// tables [0x1000, 0x9000), arena from 0x4000; image at 0x80000
	live := struct{ ramStart, ramEnd, textStart, textEnd, base, next, end uint64 }{
		ramStart: 0x80000, ramEnd: 0x3b000000,
		textStart: 0x80000, textEnd: 0x2b4000,
		base: 0x1000, next: 0x4000, end: 0x9000,
	}

	if err := ValidateLayout(live.ramStart, live.ramEnd, live.textStart,
		live.textEnd, live.base, live.next, live.end); err != nil {
		t.Fatalf("a mappable layout was rejected: %v", err)
	}

	for _, tc := range []struct {
		name                                 string
		ramStart, ramEnd, textStart, textEnd uint64
		base, next, end                      uint64
		want                                 string
	}{
		{
			name:     "ram start inside a page",
			ramStart: 0x80800, ramEnd: 0x3b000000,
			textStart: 0x80800, textEnd: 0x2b4000,
			base: 0x1000, next: 0x4000, end: 0x9000,
			want: "not 4KB-aligned",
		},
		{
			name:     "ram end inside a page",
			ramStart: 0x80000, ramEnd: 0x3b000800,
			textStart: 0x80000, textEnd: 0x2b4000,
			base: 0x1000, next: 0x4000, end: 0x9000,
			want: "not 4KB-aligned",
		},
		{
			name:     "ram region inverted",
			ramStart: 0x3b000000, ramEnd: 0x80000,
			textStart: 0x3b000000, textEnd: 0x3b001000,
			base: 0x1000, next: 0x4000, end: 0x9000,
			want: "empty or inverted",
		},
		{
			// the image linked below the region it declares
			name:     "text starts below RAM",
			ramStart: 0x80000, ramEnd: 0x3b000000,
			textStart: 0x40000, textEnd: 0x2b4000,
			base: 0x1000, next: 0x4000, end: 0x9000,
			want: "outside the RAM region",
		},
		{
			name:     "text ends above RAM",
			ramStart: 0x80000, ramEnd: 0x100000,
			textStart: 0x80000, textEnd: 0x2b4000,
			base: 0x1000, next: 0x4000, end: 0x9000,
			want: "outside the RAM region",
		},
		{
			name:     "page table base inside a page",
			ramStart: 0x80000, ramEnd: 0x3b000000,
			textStart: 0x80000, textEnd: 0x2b4000,
			base: 0x1800, next: 0x4000, end: 0x9000,
			want: "page table base",
		},
		{
			// the shape a too-low link address produces
			name:     "arena has no room",
			ramStart: 0x80000, ramEnd: 0x3b000000,
			textStart: 0x80000, textEnd: 0x2b4000,
			base: 0x1000, next: 0x9000, end: 0x9000,
			want: "no early page table space",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayout(tc.ramStart, tc.ramEnd, tc.textStart,
				tc.textEnd, tc.base, tc.next, tc.end)
			if err == nil {
				t.Fatal("accepted a layout that cannot be mapped")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
