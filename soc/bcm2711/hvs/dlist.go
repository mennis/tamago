// BCM2711 HVS5 (SCALER5) display-list construction
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package hvs constructs display list elements for the BCM2711's HVS5
// composition engine, the per-plane word sequences the hardware walks each
// frame out of its display list RAM.
//
// Rewriting that list is the cheapest way to retarget the scanout: the firmware
// brings up the clocks, the HDMI PHY and the pixel valve timing, all the parts
// that are hard to restore if they are got wrong, and then points the HVS at a
// list of planes.
//
// Broadcom documents none of it, as the peripherals datasheet omits the HVS and
// the pixel valves entirely. The layout is the Linux vc4 driver's for the
// is_vc5 path, checked against a dump taken from a live panel: the golden test
// requires word for word agreement except on the two words the hardware owns.
//
// That check has limits worth knowing, because they are not visible in a test
// that passes. A wrong shift moves a value and the panel catches it, while a
// wrong mask width stays invisible until some value reaches the bit where the
// two forms part company. Six constants are only checkable that way, and each
// records what does and does not confirm it.
//
// HVS5 is not HVS6. soc/bcm2712 has a package of the same name for the same
// job, and the two layouts are not interchangeable: this element is eight words
// carrying a size, terminated by a separate end word the CRTC flush appends,
// where HVS6's is nine with an embedded end and a next link. The header the
// layout comes from defines SCALER_, SCALER5_ and SCALER6_ forms of the same
// field names, two constants here have been the wrong form already, and both
// times the wrong one produced plausible behaviour rather than a fault.
//
// The package is pure computation with no hardware dependency, so the golden
// test runs on a host. Scaled, tiled and YUV planes are out of scope: this
// emits the unscaled, linear, raster order RGB plane a framebuffer console
// needs.
package hvs

import "fmt"

// Pixel formats, HVS_PIXEL_FORMAT_* in vc4_regs.h. Only the two a VideoCore
// framebuffer grant uses are named here; the field is five bits wide and the
// enum runs to 17.
const (
	FormatRGB565   = 4
	FormatRGBA8888 = 7
)

// Channel orders, the CTL0 ORDER field.
//
// OrderXRGB is what the golden dump carries and is the same value soc/bcm2712
// observed on every Pi 5 boot. Note this describes the SCANOUT's channel order,
// which is not the same question as the firmware's FB_GET_PIXEL_ORDER report —
// see FramebufferInfo.PixelOrder, which is deliberately not trusted.
const (
	OrderRGBX = 0
	OrderXRGB = 2
)

// End is the list terminator: CTL0 with only the END bit set. The Linux CRTC
// flush appends one after the last plane element (vc4_hvs_atomic_flush), and the
// hardware stops its walk there.
const End = 1 << 31

// CTL0 field positions (SCALER_CTL0_* and SCALER5_CTL0_* in vc4_regs.h).
//
// Three of these differ from the unprefixed form, which is a live trap in this
// header rather than a hypothetical:
//
//	UNITY          bit 15 on HVS5, bit 4 on HVS4. Checked, the scaled capture
//	               has it clear with the extra POS1 word that implies and the
//	               unity one has it set.
//	PIXEL_FORMAT   [4:0] on HVS5, [3:0] on HVS4. Unchecked: every format named
//	               here is below 16, so both readings emit the same word.
//	RGBA_EXPAND    see expandRound.
//
// SIZE, VALID, END, TILING, ORDER and ALPHA_MASK have no SCALER5 form.
const (
	ctl0Valid       = 1 << 30
	ctl0SizeShift   = 24 // SIZE[29:24]: this element's length in words
	ctl0SizeMask    = 0x3f
	ctl0TilingShift = 20      // TILING[21:20]
	ctl0AlphaMask   = 1 << 19 // unused: this port emits no per-pixel-alpha plane
	ctl0Unity       = 1 << 15 // SCALER5_CTL0_UNITY; SCALER_CTL0_UNITY is BIT(4)
	ctl0OrderShift  = 13      // ORDER[14:13]
	ctl0ExpandShift = 11      // see expandRound
	ctl0FormatMask  = 0x1f    // SCALER5 PIXEL_FORMAT[4:0]; SCALER_ is [3:0]
	tilingLinear    = 0

	// expandRound fills bits 12 and 11, which is the right word under the
	// wrong name: HVS4 reads them as a two-bit rounding enum whose value 3 is
	// ROUND, HVS5 as two independent expander enables. The vc5 path sets both
	// unconditionally, so 3 is what the capture holds and what this emits.
	//
	// The two readings agree only at that value. A caller wanting anything but
	// both expanders on must not reason from the enum.
	expandRound = 3
)

// CTL2 fields (SCALER5_CTL2_* in vc4_regs.h).
//
// There is no unprefixed form to take by mistake, as HVS4 has no CTL2 and
// splits the same information across POS2 and POS0. The latter is a trap on
// this generation, where those bits of POS0 are VFLIP and the top of START_Y:
// an HVS4-shaped opaque alpha written there both flips the plane and displaces
// it by thousands of lines.
//
// The word is checked twice and by two writers, which put the same opaque value
// in [15:4] while disagreeing about the mode.
const (
	ctl2AlphaShift     = 4  // ALPHA[15:4], 12 bits
	ctl2AlphaModeShift = 30 // ALPHA_MODE[31:30]

	// AlphaModeFixed takes the alpha from the ALPHA field rather than from the
	// pipeline. It is what the golden dump carries, and it differs from the Pi
	// 5's, which uses mode 0 — one of several places the two generations look
	// alike and are not.
	alphaModeFixed = 1

	// alphaOpaque is the largest value the 12-bit field holds.
	alphaOpaque = 0xfff
)

// POS0 fields (SCALER5_POS0_* in vc4_regs.h).
//
// This is the weakest checked block in the package: the golden element's POS0
// is zero, a plane at the origin with no flips, so the capture that anchors
// everything else says nothing here.
//
//	START_X   [13:0] on HVS5, [11:0] on HVS4. Unchecked.
//	START_Y   [27:16] on HVS5, [23:12] on HVS4. The mask is 12 bits in both,
//	          it is the shift that moved. Weakly corroborated by the firmware
//	          element, where only the HVS5 shift places the plane inside the
//	          panel.
//	HFLIP     bit 15 on HVS5, in CTL0 on HVS4. Unchecked.
//	VFLIP     bit 31 on HVS5, in CTL0 on HVS4. Unchecked.
//
// The flips are exported and have never been set. Writing them the HVS4 way
// would put VFLIP on CTL0's unity bit, which these unity planes already set, so
// the flip would silently not happen, and on a scaled element the same write
// would declare it unscaled and shift every word after POS1. HFLIP's HVS5
// meaning is unnamed in the header, which is silence rather than a guarantee.
const (
	pos0StartXMask  = 0x3fff // START_X[13:0]
	pos0StartYShift = 16     // START_Y[27:16]
	pos0StartYMask  = 0xfff
	pos0HFlip       = 1 << 15
	pos0VFlip       = 1 << 31
)

// POS2 fields (SCALER5_POS2_* in vc4_regs.h).
//
// The SCALER5 and unprefixed forms differ only in MASK width — [12:0] and
// [28:16] here against [11:0] and [27:16] on HVS4, same shifts — which is why
// the package's headline check does not reach them. 480 and 800 fit in twelve
// bits, so reproducing the capture's POS2 confirms the shifts and says nothing
// about the thirteenth bit of either field.
//
// That is the general shape of what a 480x800 panel can and cannot validate, and
// it is worth stating once: a wrong SHIFT moves the number and the panel catches
// it, a wrong MASK WIDTH only truncates values above 4095 and this panel has
// none. The consequence here is confined to Validate's bounds.
const (
	pos2WidthMask   = 0x1fff // WIDTH[12:0]
	pos2HeightShift = 16     // HEIGHT[28:16]
	pos2HeightMask  = 0x1fff
)

// Pos3Context is the Position Word 3 placeholder.
//
// The hardware owns this word at runtime: the golden dump read back something
// other than what Linux had written into it, and vc4_plane.c says the same in
// its own words, rewriting only POS0, POS2 and PTR0 on an async update rather
// than smashing the context data the HVS is using. Linux's constant is used
// here for the same reason Linux uses it, a recognisable value is easier to
// spot in a dump than a zero.
//
// It is not pitch/16, which the golden element alone would suggest: a second
// captured element, scaled and with a different pitch, separates the two, and
// the refutation survives reading that element under either word order.
//
// In both captures the high half equals the number of whole source rows between
// PTR0 and the pointer context word beside it, which reads as the row the
// scanout had reached. That is one element behind each claim, one board and one
// instant each, so treat it as a reading rather than a decode. None of it
// changes what to emit.
const Pos3Context = 0xc0c0c0c0

// PtrContext is the per-plane pointer-context word that follows PTR0.
//
// Like Pos3Context the hardware owns it: the golden dump caught it holding
// base+0xe100, which is base + 60 rows of 960 bytes and not a configured
// address. See Pos3Context — the two words appear to carry the same scan
// position, which is how each is evidence about the other, and note that the
// evidence for THAT is one capture rather than two.
const PtrContext = 0

// ElementWords is the length of one unity plane element, and therefore the value
// its CTL0 SIZE field carries: CTL0, POS0, CTL2, POS2, POS3, PTR0, PTR_CTX,
// PITCH0.
const ElementWords = 8

// ListWords is the length of the complete single-plane list Element returns: the
// element plus the END word that terminates the walk.
const ListWords = ElementWords + 1

// GPUBusAlias is the uncached VideoCore bus alias. The HVS is a VideoCore master
// and addresses memory through the bus view, so a plane's ARM physical address
// has to be translated on the way in — the golden dump's PTR0 of 0xceb00000 is
// ARM physical 0x0eb00000, the CMA buffer Linux had allocated.
//
// Getting this wrong does not fault. The HVS would scan out whatever happens to
// live at the untranslated address, which on this board is real memory, so the
// symptom is a screen full of something rather than a screen full of nothing.
const GPUBusAlias = 0xc0000000

// Plane describes one unity (unscaled), linear, raster-order plane.
type Plane struct {
	// X and Y place the plane's top-left corner on the display.
	X, Y uint32

	// Width and Height are the plane's size in pixels. On a unity plane these
	// are both the source size and the destination size, which is exactly what
	// "unity" means — there is no scaler state to program.
	Width, Height uint32

	// Pitch is the source stride in BYTES, which is not derivable from Width:
	// a firmware grant may pad rows. The golden dump's 960 for a 480-pixel
	// RGB565 row happens to be exactly 480*2, but AllocateFramebuffer returns
	// the pitch precisely because it is entitled to differ.
	Pitch uint32

	// Format is a FormatRGB565 or FormatRGBA8888 (HVS_PIXEL_FORMAT_*).
	Format uint32

	// Order is the channel order, OrderXRGB or OrderRGBX.
	Order uint32

	// Addr is the plane's ARM PHYSICAL base address. Element translates it to
	// the VideoCore bus view; callers pass what AllocateFramebuffer returned.
	Addr uint32

	// HFlip and VFlip mirror the plane. Free in the scanout, and the reason
	// they are exposed is that this panel is mounted rotated (see the bench
	// notes) — a flip in the display list costs nothing where a flip in the
	// drawing code costs a pass over every pixel.
	HFlip, VFlip bool
}

// BytesPerPixel returns the plane format's pixel size, or 0 if this package does
// not know the format.
func (p *Plane) BytesPerPixel() uint32 {
	switch p.Format {
	case FormatRGB565:
		return 2
	case FormatRGBA8888:
		return 4
	}

	return 0
}

// Validate reports whether the plane can be expressed in a display list
// element.
//
// Every field is written into its word by masking, so an out-of-range value
// does not fail, it silently becomes a different in-range value and the element
// that results looks plausible. A read-back of the list would confirm it, and
// the hardware then scans DRAM according to it.
//
// The failure really being guarded is a zero: a partly filled Plane encodes a
// degenerate element rather than an obviously broken one. This generation
// stores the raw count rather than count minus one, so a zero stays a zero
// instead of underflowing to a maximum, but that is not a reason to encode it.
//
// It is kept separate from [Plane.Element] so that the golden tests stay byte
// identical and the check has one obvious place to be called, at the hardware
// consumer before anything is written.
func (p *Plane) Validate() error {
	if p.Width == 0 || p.Height == 0 {
		return fmt.Errorf("hvs: degenerate plane %dx%d", p.Width, p.Height)
	}

	if p.Width > pos2WidthMask {
		return fmt.Errorf("hvs: width %d exceeds the %d-pixel field", p.Width, pos2WidthMask)
	}

	if p.Height > pos2HeightMask {
		return fmt.Errorf("hvs: height %d exceeds the %d-line field", p.Height, pos2HeightMask)
	}

	if p.X > pos0StartXMask {
		return fmt.Errorf("hvs: x %d exceeds the %d-pixel field", p.X, pos0StartXMask)
	}

	if p.Y > pos0StartYMask {
		return fmt.Errorf("hvs: y %d exceeds the %d-line field", p.Y, pos0StartYMask)
	}

	bpp := p.BytesPerPixel()

	if bpp == 0 {
		return fmt.Errorf("hvs: unsupported pixel format %d", p.Format)
	}

	// A pitch shorter than one row makes the scanout re-read part of the
	// previous row on every line. That renders as a shear rather than as
	// nothing, which is the kind of wrong picture that gets blamed on the
	// panel.
	if min := p.Width * bpp; p.Pitch < min {
		return fmt.Errorf("hvs: pitch %d is shorter than one %d-pixel row of %d-byte pixels (%d)",
			p.Pitch, p.Width, bpp, min)
	}

	// The address is OR-ed with the bus alias rather than added, so anything
	// already set in the top two bits survives into the pointer and silently
	// selects a different cache alias of a different address.
	if p.Addr&GPUBusAlias != 0 {
		return fmt.Errorf("hvs: plane address %#x already has bus-alias bits set", p.Addr)
	}

	return nil
}

// CTL0 returns the plane's Control Word 0.
func (p *Plane) CTL0() uint32 {
	return ctl0Valid |
		(ElementWords&ctl0SizeMask)<<ctl0SizeShift |
		tilingLinear<<ctl0TilingShift |
		ctl0Unity |
		(p.Order&0x3)<<ctl0OrderShift |
		expandRound<<ctl0ExpandShift |
		(p.Format & ctl0FormatMask)
}

// POS0 returns the plane's Position Word 0: where it lands on the display.
func (p *Plane) POS0() (v uint32) {
	v = (p.X & pos0StartXMask) | (p.Y&pos0StartYMask)<<pos0StartYShift

	if p.HFlip {
		v |= pos0HFlip
	}

	if p.VFlip {
		v |= pos0VFlip
	}

	return
}

// CTL2 returns the plane's Control Word 2: a fully opaque plane taking its alpha
// from this word rather than from the pipeline.
func (p *Plane) CTL2() uint32 {
	return alphaModeFixed<<ctl2AlphaModeShift | alphaOpaque<<ctl2AlphaShift
}

// POS2 returns the plane's Position Word 2: the source image size.
func (p *Plane) POS2() uint32 {
	return (p.Width & pos2WidthMask) |
		(p.Height&pos2HeightMask)<<pos2HeightShift
}

// PTR0 returns the plane's base pointer, translated into the VideoCore bus view
// the HVS masters memory through.
func (p *Plane) PTR0() uint32 {
	return p.Addr | GPUBusAlias
}

// Element returns the complete display list for this single plane: the eight
// element words followed by the END terminator.
//
// The two hardware-owned words (POS3 and the pointer context) are written with
// placeholders. They are part of the element's length and so cannot be omitted,
// but nothing the caller puts there survives the first frame.
func (p *Plane) Element() []uint32 {
	return []uint32{
		p.CTL0(),
		p.POS0(),
		p.CTL2(),
		p.POS2(),
		Pos3Context,
		p.PTR0(),
		PtrContext,
		p.Pitch,
		End,
	}
}

// DISPCTRLX (per-channel display control) fields, SCALER5_DISPCTRLX_* in
// vc4_regs.h.
//
// These live here rather than with the register binding for the reason the whole
// package does: soc/bcm2711 cannot be built off-target, so anything expressed
// there is unreachable by a test. That is not a hypothetical cost. This exact
// decode was first written in the binding using the UNPREFIXED
// SCALER_DISPCTRLX_* layout — vc4_regs.h carries both — which puts WIDTH at
// [23:12] instead of [28:16]. Read that way, the captured DISPCTRL0 of
// 0x81e00320 gives a 480-pixel panel a width of 3584 while the height still
// comes out a plausible 800, and half a right answer is what lets a wrong layout
// past a glance.
//
// It was caught by arithmetic against a known panel, and it stays caught by the
// test beside this file.
//
// Only half of WIDTH is caught by that, though. The two forms differ in BOTH
// shift and mask — [28:16] against [23:12] — and 480 against 3584 tests the
// shift alone. dispCtrlXWidthMask is 13 bits where HVS4 has 12, and no value
// this panel produces can tell those apart.
//
// HEIGHT is the same trap with no compensating check at all: [12:0] here against
// [11:0] on HVS4, same shift, so 800 decodes identically either way. Both masks
// read correctly today and nothing on this board can show that they do — a wrong
// mask only truncates above 4095. ENABLE has no SCALER5 form.
const (
	dispCtrlXEnable     = 1 << 31
	dispCtrlXWidthShift = 16     // SCALER5 WIDTH[28:16] shift; SCALER_ shifts by 12
	dispCtrlXWidthMask  = 0x1fff // SCALER5 WIDTH is 13 bits; SCALER_ is 12
	dispCtrlXHeightMask = 0x1fff // SCALER5 HEIGHT[12:0]; SCALER_ is [11:0]
)

// DecodeDispCtrl decodes a channel's DISPCTRLX register into its enable bit and
// output size in pixels.
//
// The size is the mode actually being scanned out, which is worth more than it
// sounds: it comes from the hardware rather than from the firmware's answer to a
// mailbox query, so it is the one geometry a bare-metal program can check
// against instead of trusting.
func DecodeDispCtrl(raw uint32) (enabled bool, width, height uint32) {
	return raw&dispCtrlXEnable != 0,
		(raw >> dispCtrlXWidthShift) & dispCtrlXWidthMask,
		raw & dispCtrlXHeightMask
}
