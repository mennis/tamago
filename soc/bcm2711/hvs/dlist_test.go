// BCM2711 HVS5 (SCALER5) display-list construction
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package hvs

import "testing"

// The golden capture: HVS channel 0's display list read out of a Raspberry Pi 4
// Model B while Linux was scanning out a live 480x800 panel, on 2026-07-31.
// Nine words at display-list index 54, verbatim.
//
// This is the whole basis for the package. Nothing in the BCM2711 datasheet
// describes the HVS, so the alternative to a capture is inference from a driver
// written for a different generation, and the failure mode of getting it wrong
// is a black screen with no fault and nothing logged.
var golden = []uint32{
	0x4800d804, // 54: CTL0
	0x00000000, // 55: POS0
	0x4000fff0, // 56: CTL2
	0x032001e0, // 57: POS2
	0x003c0000, // 58: POS3      — hardware-owned, see Pos3Context
	0xceb00000, // 59: PTR0
	0xceb0e100, // 60: PTR_CTX   — hardware-owned, see PtrContext
	0x000003c0, // 61: PITCH0
	0x80000000, // 62: END
}

// goldenPlane is that same plane described in the terms this package takes.
//
// Be precise about which of these are independent of the capture, because the
// test is only worth what that distinction is worth:
//
//   - Width, Height, Pitch and Format ARE independent. 480x800 is the panel's
//     mode as kmsprint reported it, 960 is the pitch the firmware reports for
//     that mode, and RGB565 follows from the grant being 16bpp. None was read
//     out of the dump, and the test's substance is that they reproduce CTL0,
//     POS2 and PITCH0 exactly.
//
//   - Order and Addr are NOT. OrderXRGB was read off the capture's CTL0 (its own
//     doc comment says so), and Addr is the capture's PTR0 with the bus alias
//     masked off. Where those two feed a check, the check is definitional rather
//     than evidential — TestPTR0TranslatesToTheBusView in particular is circular
//     and is really testing the masking arithmetic, not the address.
//
// An earlier version of this comment claimed none of these numbers came from the
// dump. That was false of Order and Addr, and a test whose comment overstates
// what it proves is worse than a weaker test that says so.
var goldenPlane = Plane{
	Width:  480,
	Height: 800,
	Pitch:  960,
	Format: FormatRGB565,
	Order:  OrderXRGB,
	Addr:   0x0eb00000,
}

// Element words by index. The two the hardware owns are POS3 and PTR_CTX; the
// other two are named because the scan-position tests below read them.
const (
	idxPOS3   = 4
	idxPTR0   = 5
	idxPTRCTX = 6
	idxPITCH0 = 7
)

// goldenFirmware is a second captured element, the list the firmware left on
// channel 0 of the same board, read from bare metal.
//
// It earns its place by being a different shape of element, which is the only
// thing that can tell a field apart from a constant that happens to equal it: a
// small scaled source against the golden capture's unscaled full screen one.
//
// # It is not the layout the log printed
//
// The log labels these nine words with the unity order this port emits, and that
// order is wrong here — which is visible in the log itself as a PITCH0 of
// 4273971232 bytes per row. CTL0 says why: bit 15 (SCALER5_CTL0_UNITY) is CLEAR
// and SIZE is 16, so this is a scaled element. A scaled element carries POS1
// between CTL2 and POS2 — see vc4_plane.c:1548-1570, where the gen5 branch emits
// CTL0, POS0, CTL2, then POS1 only `if (!vc4_state->is_unity)`, then POS2 — so
// everything from index 3 on is one word further along than the labels claim.
//
// # What actually discriminates
//
// Only the words AFTER the shift point can, and there are two findings among
// them — on partly overlapping words rather than independent ones, since both
// draw on 0xfebfa000 and 0xfebfa020:
//
//   - PITCH0 becomes 32, which is a pitch. Under the unity order it is
//     0xfebfa020, which CANNOT be one: SCALER_SRC_PITCH is VC4_MASK(15, 0), and
//     this element's CTL0 has TILING 0, so a linear plane's pitch word has to
//     have [31:16] clear. 0xfebfa020 carries 0xfebf there. That is the driver's
//     own field definition rather than an appeal to plausibility, and it is
//     decisive on its own.
//   - The pointer pair becomes coherent. PTR0 is 0xfebfa000, physical
//     0x3ebfa000 once the bus alias is stripped, which is INSIDE the 768000-byte
//     framebuffer the firmware granted at 0x3eb3f000 in that same run — 2048
//     bytes short of its end. PTR_CTX is then PTR0 + 32, one row. Under the
//     unity order the same words give an unaliased PTR0 of 0x00010000 in the
//     ARM's first 64KB, with PTR_CTX 0xfebea000 — about 4.3GB — past it, which
//     is not a scan offset into anything.
//
// POS2 becoming a 2x2 source is consistent with the re-reading and does not
// discriminate; nothing rules out the unity order's POS3 of 0x00020002.
//
// # What does NOT discriminate, and was claimed to
//
// An earlier version of this comment led with "POS1 becomes 480x480 and POS0's
// START_Y becomes 160, and 160 + 480 + 160 is exactly 800". That is not
// evidence. POS0 is index 1, BEFORE the shift point, so START_Y reads 160 under
// both orders; and the unity order puts 0x01e001e0 in POS2, where a unity plane's
// source size equals its destination size — so it draws the same centred 480x480
// square. The reading being refuted produces that arithmetic just as neatly.
// (It also assumes the stale channel-0 list was configured for the panel channel
// 1 was driving: channel 0 is disabled in that capture with a zeroed DISPCTRL0,
// so the 800 is channel 1's.)
//
// # What it is not
//
// A frozen list on a channel the firmware left DISABLED. The values are what the
// HVS last wrote before the channel stopped, not something observed live, and
// strictly they could have been written by the firmware rather than by the
// scanout. That does not weaken the use made of it below: whoever wrote POS3, it
// does not hold pitch/16.
//
// The trailing seven words (SIZE is 16) were not dumped. They would be scaling
// parameters, and Linux's emitter would not have produced 16 of them — the
// element says so itself. CTL0's SCL0 [7:5] and SCL1 [10:8] are both 0, which is
// SCALER_CTL0_SCL_H_PPF_V_PPF, and PPF on both axes is also what
// vc4_get_scaling_mode returns for 2 -> 480. Counting vc4_plane.c for that case
// — nine words here, the LBM slot, one H-PPF word, two V-PPF (parameters plus
// its own context word), four kernel offsets — gives exactly 17.
//
// So this element is the hardware's layout rather than word-for-word Linux's,
// which is why the word order above is cited to the driver for POS1's PLACEMENT
// and to CTL0's UNITY bit for its PRESENCE. Only the first nine words are used
// here.
var goldenFirmware = struct {
	CTL0, POS0, CTL2, POS1, POS2, POS3, PTR0, PTRCTX, PITCH0 uint32
}{
	CTL0:   0x50805804,
	POS0:   0x00a00000,
	CTL2:   0x0000fff0,
	POS1:   0x01e001e0,
	POS2:   0x00020002,
	POS3:   0x00010000,
	PTR0:   0xfebfa000,
	PTRCTX: 0xfebfa020,
	PITCH0: 0x00000020,
}

// POS3's high half and the pointer context are one scan position, not two
// independent words.
//
// In each capture, POS3[31:16] equals the whole number of source rows between
// PTR0 and PTR_CTX. That is the claim Pos3Context rests on, and it is the reason
// the golden test excludes both words: they are a timestamp.
//
// Read this together with TestPos3IsNotThePitchOverSixteen. On its own the
// golden capture cannot distinguish the two readings — this test passes under
// either — because 960/16 and the line the beam was on are both 60.
func TestPos3AndPtrContextCarryOneScanPosition(t *testing.T) {
	for _, tc := range []struct {
		name               string
		pos3, ptr0, ptrCtx uint32
		pitch              uint32
		wantRow            uint32
	}{
		{"Linux, unity, live", golden[idxPOS3], golden[idxPTR0], golden[idxPTRCTX], golden[idxPITCH0], 60},
		{"firmware, scaled, frozen", goldenFirmware.POS3, goldenFirmware.PTR0, goldenFirmware.PTRCTX, goldenFirmware.PITCH0, 1},
	} {
		row := tc.pos3 >> 16

		if row != tc.wantRow {
			t.Errorf("%s: POS3 high half = %d, capture has %d", tc.name, row, tc.wantRow)
		}

		if off := tc.ptrCtx - tc.ptr0; off != row*tc.pitch {
			t.Errorf("%s: PTR_CTX is %#x past PTR0, which is not row %d of pitch %d (%#x)",
				tc.name, off, row, tc.pitch, row*tc.pitch)
		}

		// Both captures caught the element at the START of a row: the pointer
		// offset divides exactly and POS3's low half is zero. Whatever the low
		// half counts within a line, neither sample says.
		if tc.pos3&0xffff != 0 {
			t.Errorf("%s: POS3 low half = %#x, both captures have 0 — if this ever"+
				" fires the low half means something and Pos3Context's note is stale",
				tc.name, tc.pos3&0xffff)
		}
	}
}

// POS3's high half is NOT the pitch divided by sixteen.
//
// It looked like it: the golden capture's 60 is exactly 960/16, on a word this
// package treats as scratch, and a field encoding the pitch would be
// configuration that a scaled or tiled plane might have to supply.
//
// The second capture separates the two. Its pitch is 32, so pitch/16 is 2, and
// POS3's high half is 1 — which is the row PTR_CTX points at. The GOLDEN capture
// alone could not have told these apart, since its 60 satisfies both readings at
// once; that is the whole reason a second element with a different pitch is worth
// keeping in this file. (The firmware element alone DOES tell them apart, which
// is why the refutation rests on it and not on the pair.)
func TestPos3IsNotThePitchOverSixteen(t *testing.T) {
	const (
		pitch = 32
		row   = 1
	)

	if goldenFirmware.PITCH0 != pitch || goldenFirmware.POS3>>16 != row {
		t.Fatalf("the capture this argument rests on has changed: pitch %d, POS3 high %d",
			goldenFirmware.PITCH0, goldenFirmware.POS3>>16)
	}

	if goldenFirmware.POS3>>16 == goldenFirmware.PITCH0/16 {
		t.Error("POS3 high half equals pitch/16 in the scaled capture too:" +
			" the reading this test exists to refute is back in play")
	}
}

// The refutation does not depend on the word order the second element is read
// with, which matters because that order is the one thing about goldenFirmware
// that is inferred rather than transcribed.
//
// Read it the way its own log labelled it — unity, so POS3 at index 4 and PITCH0
// at index 7 — and those words are 0x00020002 and 0xfebfa020, giving a high half
// of 2 against a pitch/16 of 267123202. The scaled reading gives 1 against 2.
// Neither order supports pitch/16, so if the re-reading in goldenFirmware's
// comment were wrong, the conclusion would still stand.
func TestPos3IsNotThePitchUnderEitherWordOrder(t *testing.T) {
	// The nine words in log order, so this test does not depend on the struct's
	// interpretation of them.
	raw := []uint32{
		0x50805804, 0x00a00000, 0x0000fff0, 0x01e001e0, 0x00020002,
		0x00010000, 0xfebfa000, 0xfebfa020, 0x00000020,
	}

	// Both assertions below are INEQUALITIES, so a stale or mistyped raw would
	// keep passing and quietly stop testing the capture. Tie it to the struct,
	// which is the transcription the rest of the file is checked against.
	for i, w := range []uint32{
		goldenFirmware.CTL0, goldenFirmware.POS0, goldenFirmware.CTL2,
		goldenFirmware.POS1, goldenFirmware.POS2, goldenFirmware.POS3,
		goldenFirmware.PTR0, goldenFirmware.PTRCTX, goldenFirmware.PITCH0,
	} {
		if raw[i] != w {
			t.Fatalf("raw[%d] = %#08x but goldenFirmware has %#08x: the two"+
				" transcriptions of the same nine words have drifted", i, raw[i], w)
		}
	}

	for _, tc := range []struct {
		order          string
		idxPOS3, idxPI int
	}{
		{"unity, as the log labelled it", 4, 7},
		{"scaled, as goldenFirmware reads it", 5, 8},
	} {
		pos3, pitch := raw[tc.idxPOS3], raw[tc.idxPI]

		if pitch != 0 && pos3>>16 == pitch/16 {
			t.Errorf("%s: POS3 high half %d equals pitch/16", tc.order, pos3>>16)
		}
	}
}

// The firmware's element is scaled, and reading it as a unity one is what hid
// the pitch above.
//
// Pinned because the whole of goldenFirmware's word order follows from CTL0's
// UNITY bit and SIZE. Be clear about which checks below carry weight: the pitch
// and the pointer pair are the ones that separate the two orders, and the
// geometry checks are internal-consistency only — the unity order produces the
// same centred 480x480 square, as goldenFirmware's comment says.
func TestFirmwareElementIsScaledNotUnity(t *testing.T) {
	if v := goldenFirmware.CTL0; v&ctl0Unity != 0 {
		t.Fatalf("CTL0 %#08x has UNITY set; the element would then have no POS1"+
			" and the word order used here is wrong", v)
	}

	if got := (goldenFirmware.CTL0 >> ctl0SizeShift) & ctl0SizeMask; got != 16 {
		t.Errorf("CTL0 SIZE = %d, the capture has 16", got)
	}

	// Consistency, not discrimination: a 480x480 destination at y=160 leaves 160
	// lines above and below on the panel channel 1 was driving. The unity order
	// yields the same square from the same words.
	if w, h := goldenFirmware.POS1&0x1fff, (goldenFirmware.POS1>>16)&0x1fff; w != 480 || h != 480 {
		t.Errorf("POS1 scaled size = %dx%d, want 480x480", w, h)
	}

	if y := (goldenFirmware.POS0 >> pos0StartYShift) & pos0StartYMask; y != 160 || 2*y+480 != 800 {
		t.Errorf("POS0 START_Y = %d, want 160 — a 480-tall plane centred on 800 lines", y)
	}

	if w, h := goldenFirmware.POS2&pos2WidthMask, (goldenFirmware.POS2>>pos2HeightShift)&pos2HeightMask; w != 2 || h != 2 {
		t.Errorf("POS2 source size = %dx%d, want 2x2", w, h)
	}

	// This one does discriminate. The pointer is bus-aliased and lands inside
	// the framebuffer the same run's firmware granted, 2048 bytes short of its
	// end, with PTR_CTX exactly one row further on.
	if goldenFirmware.PTR0&GPUBusAlias != GPUBusAlias {
		t.Errorf("PTR0 %#08x does not carry the bus alias", goldenFirmware.PTR0)
	}

	const (
		grant     = 0x3eb3f000 // the run's firmware framebuffer
		grantSize = 480 * 800 * 2
	)

	phys := goldenFirmware.PTR0 &^ uint32(GPUBusAlias)

	if phys != 0x3ebfa000 {
		t.Errorf("PTR0 physical = %#08x, want 0x3ebfa000", phys)
	}

	if phys < grant || phys >= grant+grantSize {
		t.Errorf("PTR0 physical %#08x is outside the firmware grant [%#x, %#x)",
			phys, grant, grant+grantSize)
	}

	// The alpha field sits where CTL2 says it does under a second, independent
	// writer: the firmware asked for opaque too, with a different alpha MODE.
	if a := (goldenFirmware.CTL2 >> ctl2AlphaShift) & alphaOpaque; a != alphaOpaque {
		t.Errorf("CTL2 ALPHA = %#x, want the opaque %#x", a, alphaOpaque)
	}
}

// The element must match the capture word for word, except where the hardware
// owns the word.
func TestElementMatchesSilicon(t *testing.T) {
	got := goldenPlane.Element()

	if len(got) != len(golden) {
		t.Fatalf("Element() returned %d words, the capture has %d", len(got), len(golden))
	}

	// CTL2 is a constant function of nothing — it reads no Plane field — so its
	// agreement with the capture is definitional, not derived. It is still worth
	// comparing (the constant could be wrong), but it is not evidence that the
	// decode understood anything.
	names := []string{"CTL0", "POS0", "CTL2", "POS2", "POS3", "PTR0", "PTR_CTX", "PITCH0", "END"}

	for i := range golden {
		if i == idxPOS3 || i == idxPTRCTX {
			// Written by the scanout every frame. The capture caught mid-frame
			// state (POS3 read 0x003c0000 where Linux had written 0xc0c0c0c0,
			// and PTR_CTX read base+0xe100, a scan position), so requiring
			// agreement here would be requiring agreement with a timestamp.
			continue
		}

		if got[i] != golden[i] {
			t.Errorf("word %d (%s) = %#08x, silicon has %#08x",
				i, names[i], got[i], golden[i])
		}
	}
}

// Each authored word is also checked on its own, so a failure names the field
// rather than just the offset. Written as independent derivations of the same
// capture: if CTL0 is wrong, this says whether the format, the order or the size
// is what went wrong.
func TestCTL0Fields(t *testing.T) {
	v := goldenPlane.CTL0()

	for _, tc := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"END", v >> 31, 0},
		{"VALID", (v >> 30) & 1, 1},
		{"SIZE", (v >> 24) & 0x3f, ElementWords},
		{"TILING", (v >> 20) & 0x3, tilingLinear},
		{"ALPHA_MASK", (v >> 19) & 1, 0},
		{"UNITY", (v >> 15) & 1, 1},
		{"ORDER", (v >> 13) & 0x3, OrderXRGB},
		{"RGBA_EXPAND", (v >> 11) & 0x3, expandRound},
		{"PIXEL_FORMAT", v & 0x1f, FormatRGB565},
	} {
		if tc.got != tc.want {
			t.Errorf("CTL0 %s = %#x, want %#x", tc.name, tc.got, tc.want)
		}
	}

	if v != golden[0] {
		t.Errorf("CTL0 = %#08x, silicon has %#08x", v, golden[0])
	}
}

// The geometry words must carry the panel's dimensions, not something that
// merely happens to add up. 480 and 800 are the numbers to look for.
func TestPOS2CarriesPanelGeometry(t *testing.T) {
	v := goldenPlane.POS2()

	if w := v & pos2WidthMask; w != 480 {
		t.Errorf("POS2 WIDTH = %d, want 480", w)
	}

	if h := (v >> pos2HeightShift) & pos2HeightMask; h != 800 {
		t.Errorf("POS2 HEIGHT = %d, want 800", h)
	}
}

// A plane at the origin with no flips has an all-zero POS0, which is worth
// pinning because it is the one word whose correctness a dump cannot show:
// zero is what an unwritten word looks like too.
func TestPOS0Origin(t *testing.T) {
	if v := goldenPlane.POS0(); v != 0 {
		t.Errorf("POS0 at the origin = %#08x, want 0", v)
	}
}

func TestPOS0PlacementAndFlips(t *testing.T) {
	p := goldenPlane
	p.X, p.Y = 100, 200

	v := p.POS0()

	if x := v & pos0StartXMask; x != 100 {
		t.Errorf("POS0 START_X = %d, want 100", x)
	}

	if y := (v >> pos0StartYShift) & pos0StartYMask; y != 200 {
		t.Errorf("POS0 START_Y = %d, want 200", y)
	}

	// The flips are separate bits at opposite ends of the word, which is
	// exactly the shape of mistake that puts one where the other goes.
	p.HFlip = true
	if v := p.POS0(); v&pos0HFlip == 0 || v&pos0VFlip != 0 {
		t.Errorf("HFlip alone = %#08x: wrong bit, or both bits", v)
	}

	p.HFlip, p.VFlip = false, true
	if v := p.POS0(); v&pos0VFlip == 0 || v&pos0HFlip != 0 {
		t.Errorf("VFlip alone = %#08x: wrong bit, or both bits", v)
	}
}

// The address must be translated into the bus view. An untranslated pointer does
// not fault — the HVS scans out whatever lives at that ARM address — so the
// arithmetic is worth pinning.
//
// CIRCULAR, and kept anyway with that said out loud: goldenPlane.Addr IS the
// capture's PTR0 with the alias masked off, so this asserts that masking and
// then OR-ing returns the original. That is a real property of the two
// operations and a real thing to break, but it is not independent evidence that
// 0xceb00000 is the right pointer.
func TestPTR0TranslatesToTheBusView(t *testing.T) {
	if v := goldenPlane.PTR0(); v != 0xceb00000 {
		t.Errorf("PTR0 = %#08x, silicon has 0xceb00000", v)
	}

	if goldenPlane.PTR0()&^GPUBusAlias != goldenPlane.Addr {
		t.Error("PTR0 does not carry the physical address it was given")
	}
}

// The pitch is passed through as bytes, and is deliberately NOT derived from the
// width: a firmware grant may pad rows, and AllocateFramebuffer reports the
// pitch separately for that reason. A pitch that silently equalled Width*bpp
// would work on this panel and tear on the first one that pads.
func TestPitchIsNotDerivedFromWidth(t *testing.T) {
	p := goldenPlane
	p.Pitch = 1024 // padded, as a firmware grant is entitled to return

	if got := p.Element()[7]; got != 1024 {
		t.Errorf("PITCH0 = %d, want the pitch as given (1024)", got)
	}
}

// The element's declared SIZE has to equal its actual length, or the hardware
// walks into the middle of the next element.
func TestDeclaredSizeMatchesActualLength(t *testing.T) {
	el := goldenPlane.Element()

	if got := (el[0] >> 24) & 0x3f; got != uint32(len(el)-1) {
		t.Errorf("CTL0 SIZE = %d, element is %d words before the terminator",
			got, len(el)-1)
	}

	if len(el) != ListWords {
		t.Errorf("Element() is %d words, ListWords says %d", len(el), ListWords)
	}

	if el[len(el)-1] != End {
		t.Errorf("list does not end with the terminator: %#08x", el[len(el)-1])
	}
}

// RGBA8888 is the other format a grant can use, and the format field is the one
// place a wrong value produces a recognisable wrong picture rather than nothing.
func TestFormatSelectsThePixelFormatField(t *testing.T) {
	p := goldenPlane
	p.Format = FormatRGBA8888

	if got := p.CTL0() & 0x1f; got != FormatRGBA8888 {
		t.Errorf("CTL0 PIXEL_FORMAT = %d, want %d", got, FormatRGBA8888)
	}
}

// The two DISPCTRLX values captured alongside the display list. Channel 0 was
// driving the live 480x800 panel over HDMI0; channel 1 was configured for PAL
// and disabled, which independently corroborates the decode — 720x576 is exactly
// the mode this board's pixelvalve2 was found stuck in during bring-up, before
// hdmi_timings forced the panel's own.
func TestDecodeDispCtrl(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     uint32
		enabled bool
		w, h    uint32
	}{
		{"channel 0, live panel", 0x81e00320, true, 480, 800},
		{"channel 1, PAL, disabled", 0x42d00240, false, 720, 576},
		{"channel 2, unconfigured", 0x00000000, false, 0, 0},
	} {
		enabled, w, h := DecodeDispCtrl(tc.raw)

		if enabled != tc.enabled || w != tc.w || h != tc.h {
			t.Errorf("DecodeDispCtrl(%#08x) [%s] = (%t, %d, %d), want (%t, %d, %d)",
				tc.raw, tc.name, enabled, w, h, tc.enabled, tc.w, tc.h)
		}
	}
}

// Every field layout in this package must be the SCALER5 form, and this names
// the SCALER (HVS4) value each one must not become.
//
// This is tamago issue #220 written as code. vc4_regs.h defines both forms of
// several constants four characters apart, this package has taken the wrong one
// twice, and both times the wrong constant produced plausible behaviour rather
// than a fault — which is what let it survive. A constant that is merely correct
// today records nothing about which of the two it is.
//
// Note what this test is and is not. It compares constants to constants, so it
// cannot show that the SCALER5 form is the right one for this silicon; the
// captures do that where they can. What it does is fail loudly, naming the trap,
// if a future edit reaches for the unprefixed value — including for the six
// fields below that NO capture from this board can discriminate, marked with the
// reason.
func TestFieldLayoutsAreTheSCALER5Forms(t *testing.T) {
	for _, tc := range []struct {
		field string
		got   uint32
		hvs4  uint32
		// checked says what makes the choice evidence rather than assertion,
		// or is empty where nothing on this board can tell the two apart.
		checked string
	}{
		{"CTL0 UNITY", ctl0Unity, 1 << 4,
			"set in the golden unity element, clear in the firmware's scaled one"},
		{"CTL0 PIXEL_FORMAT mask", ctl0FormatMask, 0xf, ""},
		{"POS0 START_X mask", pos0StartXMask, 0xfff, ""},
		{"POS0 START_Y shift", pos0StartYShift, 12,
			"weak: the firmware element's START_Y is 160 here and 2560 under HVS4, and only" +
				" 160 fits a 480-tall plane — but the 800 is channel 1's and that element sits" +
				" on a disabled channel 0"},
		{"POS2 WIDTH mask", pos2WidthMask, 0xfff, ""},
		{"POS2 HEIGHT mask", pos2HeightMask, 0xfff, ""},
		{"DISPCTRLX WIDTH shift", dispCtrlXWidthShift, 12,
			"the live channel decodes as 480 rather than 3584"},
		// The 480-vs-3584 result tests the shift only. WIDTH's mask moved too,
		// and no value this panel produces separates 13 bits from 12.
		{"DISPCTRLX WIDTH mask", dispCtrlXWidthMask, 0xfff, ""},
		{"DISPCTRLX HEIGHT mask", dispCtrlXHeightMask, 0xfff, ""},
	} {
		if tc.got == tc.hvs4 {
			t.Errorf("%s = %#x, which is the SCALER (HVS4) value — vc4_regs.h has a"+
				" SCALER5_ form four characters away and this SoC is HVS5", tc.field, tc.got)
		}

		if tc.checked == "" {
			t.Logf("%s: %#x, unvalidated — a 480x800 panel cannot tell it from %#x",
				tc.field, tc.got, tc.hvs4)
		}
	}

	// POS2's two shifts are 16 and 0 under BOTH forms, so the capture's 480x800
	// confirms them and still says nothing about the masks above.

	// The flips have no capture behind them at all. On HVS4 they are in CTL0 at
	// bits 16 and 15; bit 15 is HVS5's UNITY, so writing VFLIP the HVS4 way is a
	// no-op on the unity planes this package emits and would SET UNITY on a
	// scaled one, displacing every word after POS1. Bit 16 is not named for HVS5
	// at all, which makes an HVS4-style HFLIP undefined rather than harmless.
	// Checked as behaviour rather than as a constant, because that is where it
	// would go wrong: setting a flip must move POS0 and leave CTL0 alone.
	p := goldenPlane
	p.HFlip, p.VFlip = true, true

	if got := p.CTL0(); got != goldenPlane.CTL0() {
		t.Errorf("flipping changed CTL0 to %#08x: the flips belong in POS0 on HVS5,"+
			" and CTL0 bit 15 is UNITY", got)
	}

	if p.CTL0()&ctl0Unity == 0 {
		t.Error("CTL0 UNITY is clear on a flipped unity plane")
	}

	if got := p.POS0(); got&(pos0HFlip|pos0VFlip) != pos0HFlip|pos0VFlip {
		t.Errorf("POS0 = %#08x, both flip bits should be set", got)
	}
}

// The regression that motivated moving this decode into a testable package: the
// older SCALER_DISPCTRLX layout puts WIDTH at [23:12], and reading the live
// channel that way yields 3584 rather than 480 while the height still looks
// right. Pinned so the wrong layout cannot come back quietly.
func TestDispCtrlUsesTheSCALER5Layout(t *testing.T) {
	const live = 0x81e00320

	if _, w, _ := DecodeDispCtrl(live); w == 3584 {
		t.Fatal("WIDTH decoded as 3584: the SCALER (non-5) [23:12] layout is back")
	}

	if _, w, h := DecodeDispCtrl(live); w != 480 || h != 800 {
		t.Errorf("live channel decoded as %dx%d, the panel is 480x800", w, h)
	}
}

// Every field is written into its word by masking, so an out-of-range value does
// not fail — it silently becomes a different, in-range value, and the resulting
// element looks entirely plausible. A read-back of the display list would confirm
// it. The hardware then scans DRAM according to it.
//
// Mirrors tamago issue #172, filed against the BCM2712 package for the same
// omission. These cases are what that issue's acceptance criteria ask for,
// translated to this generation's field widths.
func TestValidateRejectsWhatMaskingWouldHide(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plane)
	}{
		{"zero width", func(p *Plane) { p.Width = 0 }},
		{"zero height", func(p *Plane) { p.Height = 0 }},
		{"width past the 13-bit field", func(p *Plane) { p.Width = 0x2000 }},
		{"height past the 13-bit field", func(p *Plane) { p.Height = 0x2000 }},
		{"x past the 14-bit field", func(p *Plane) { p.X = 0x4000 }},
		{"y past the 12-bit field", func(p *Plane) { p.Y = 0x1000 }},
		{"unknown pixel format", func(p *Plane) { p.Format = 13 }},
		{"pitch shorter than a row", func(p *Plane) { p.Pitch = 959 }},
		{"address carrying bus-alias bits", func(p *Plane) { p.Addr |= GPUBusAlias }},
	} {
		p := goldenPlane
		tc.mutate(&p)

		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// The mode the port actually runs must pass, or the check is just an obstacle.
func TestValidateAcceptsTheGoldenPlane(t *testing.T) {
	if err := goldenPlane.Validate(); err != nil {
		t.Fatalf("the plane captured from silicon was rejected: %v", err)
	}
}

// Demonstrates what Validate is protecting against, rather than asserting it in
// prose: an uninitialized-height plane encodes a perfectly well-formed element.
// Nothing downstream can tell it is wrong.
func TestDegenerateGeometryStillEncodesCleanly(t *testing.T) {
	p := goldenPlane
	p.Height = 0

	el := p.Element()

	if el[0] != golden[0] {
		t.Fatal("expected CTL0 to be unaffected by height")
	}

	if got := (el[3] >> pos2HeightShift) & pos2HeightMask; got != 0 {
		t.Fatalf("POS2 height encoded as %d", got)
	}

	// The point: the element is valid-looking and the terminator is intact, so
	// a read-back verification of this list passes.
	if el[len(el)-1] != End {
		t.Fatal("expected a well-formed list despite the degenerate height")
	}

	if err := p.Validate(); err == nil {
		t.Error("Validate is the only thing that can catch this, and it did not")
	}
}
