// BCM2711 PixelValve timing
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package pv encodes and decodes BCM2711 pixel valve timing, the registers that
// turn a display mode into the horizontal and vertical intervals the scanout
// counts against.
//
// The HVS composes pixels, the pixel valve times them, and the HDMI block
// serialises them, so this is the second step of an ARM side mode set:
// soc/bcm2711/hvs chooses what is scanned out and this chooses the shape of the
// raster.
//
// It is encode and decode only, with no hardware in it. Programming timing is
// the point at which a mistake stops being recoverable by looking at the
// screen, as a wrong interval produces no picture and no error, so the
// arithmetic is kept where it can be checked against a mode whose numbers are
// known.
//
// Broadcom documents none of this block. The layout is the Linux vc4 driver's
// and every field is checked against a pixel valve read out of a board while
// Linux drove a panel whose mode line was measured independently, so the eight
// timing numbers can be derived from the mode and required to equal the
// captured registers. The totals give a third check, as they reproduce the
// refresh rate the mode line states and nothing in the decode was fitted to it.
package pv

import "fmt"

// Register offsets from a pixelvalve's base.
const (
	CONTROL    = 0x00
	V_CONTROL  = 0x04
	VSYNCD_EVN = 0x08
	HORZA      = 0x0c
	HORZB      = 0x10
	VERTA      = 0x14
	VERTB      = 0x18
	VERTA_EVEN = 0x1c
	VERTB_EVEN = 0x20
	INTEN      = 0x24
	INTSTAT    = 0x28
	STAT       = 0x2c
	HACT_ACT   = 0x30
)

// PV_CONTROL fields.
const (
	ControlEN               = 1 << 0
	ControlFIFOClr          = 1 << 1
	ControlClkSelectShift   = 2 // CLK_SELECT[3:2]
	ControlClkSelectMask    = 0x3
	ControlPixelRepShift    = 4 // PIXEL_REP[5:4]
	ControlPixelRepMask     = 0x3
	ControlWaitHStart       = 1 << 12
	ControlTriggerUnderflow = 1 << 13
	ControlClrAtStart       = 1 << 14
	ControlFIFOLevelShift   = 15 // FIFO_LEVEL[20:15]
	ControlFIFOLevelMask    = 0x3f
	ControlFormatShift      = 21 // FORMAT[23:21]
	ControlFormatMask       = 0x7

	// ControlFIFOLevelHighShift is the PV5 extension of the FIFO level field.
	// Its presence is why a BCM2711 PV_CONTROL cannot be read with the plain
	// vc4 layout and expected to make sense.
	ControlFIFOLevelHighShift = 25 // PV5_FIFO_LEVEL_HIGH[26:25]
	ControlFIFOLevelHighMask  = 0x3
)

// PV_CONTROL FORMAT values.
const (
	Format24BPP = 0
)

// PV_V_CONTROL fields.
const (
	VControlVIDEN      = 1 << 0
	VControlContinuous = 1 << 1
	VControlCommand    = 1 << 2
	VControlDSI        = 1 << 3
	VControlInterlace  = 1 << 4
	VControlOddFirst   = 1 << 5
	VControlOddTiming  = 1 << 29
)

// HDMIPixelsPerClock is how many pixels this SoC's HDMI path moves per
// pixelvalve clock, and therefore the divisor applied to every HORIZONTAL
// interval. Vertical intervals are counted in lines and are NOT divided.
//
// This is measured, not read out of a register. The capture's PV_CONTROL has
// PIXEL_REP set to 0, so the halving does not come from the field whose name
// suggests it — it is a property of how the BCM2711 wires its HDMI controller to
// the pixelvalve, and the evidence for it is simply that the captured horizontal
// values are exactly half the panel's and the vertical ones are exactly equal.
//
// Asymmetric division is the kind of thing that looks like a bug in a driver and
// is not, so it is named rather than buried in an expression.
const HDMIPixelsPerClock = 2

// Timing is a display mode expressed the way a mode line does: active extent
// plus the three blanking intervals on each axis.
type Timing struct {
	HActive, HFrontPorch, HSync, HBackPorch uint32
	VActive, VFrontPorch, VSync, VBackPorch uint32

	// PixelClock is the dot clock in Hz. It does not appear in any pixelvalve
	// register — the pixelvalve counts intervals and something else supplies
	// the clock — but it belongs with the rest of the mode, and Refresh needs
	// it.
	PixelClock uint32

	// PixelsPerClock divides the horizontal intervals on the way into the
	// registers. Use HDMIPixelsPerClock for the HDMI path; 1 elsewhere. Zero
	// is treated as 1 so a zero value is not silently a divide by zero.
	PixelsPerClock uint32
}

// perClock returns the horizontal divisor, treating 0 as 1.
func (t *Timing) perClock() uint32 {
	if t.PixelsPerClock == 0 {
		return 1
	}

	return t.PixelsPerClock
}

// HTotal and VTotal are the full raster extents including blanking.
func (t *Timing) HTotal() uint32 {
	return t.HActive + t.HFrontPorch + t.HSync + t.HBackPorch
}

func (t *Timing) VTotal() uint32 {
	return t.VActive + t.VFrontPorch + t.VSync + t.VBackPorch
}

// Refresh returns the frame rate in millihertz.
//
// Millihertz rather than a float because this is the arithmetic that says
// whether a mode is the one intended, and 62.29Hz versus 62Hz is the difference
// between a panel locking and not. Integer millihertz compares exactly.
func (t *Timing) Refresh() uint32 {
	total := uint64(t.HTotal()) * uint64(t.VTotal())

	if total == 0 {
		return 0
	}

	return uint32(uint64(t.PixelClock) * 1000 / total)
}

// Valid reports whether the timing can be expressed in the registers.
//
// Every interval lands in a 16-bit field, and a value that does not fit does not
// fault — it truncates, producing a raster subtly the wrong shape, which is
// diagnosable only by photographing the panel. So it is refused here.
func (t *Timing) Valid() error {
	div := t.perClock()

	for _, f := range []struct {
		name string
		v    uint32
	}{
		{"HActive", t.HActive / div},
		{"HFrontPorch", t.HFrontPorch / div},
		{"HSync", t.HSync / div},
		{"HBackPorch", t.HBackPorch / div},
		{"VActive", t.VActive},
		{"VFrontPorch", t.VFrontPorch},
		{"VSync", t.VSync},
		{"VBackPorch", t.VBackPorch},
	} {
		if f.v > 0xffff {
			return fmt.Errorf("pv: %s of %d does not fit the 16-bit field", f.name, f.v)
		}
	}

	if t.HActive == 0 || t.VActive == 0 {
		return fmt.Errorf("pv: mode has no active area (%dx%d)", t.HActive, t.VActive)
	}

	// A horizontal interval that is not a whole number of pixelvalve clocks
	// cannot be programmed. Truncating would shift every subsequent interval
	// and move the picture, so an odd value on a 2-pixel-per-clock path is an
	// error rather than something to round.
	if div > 1 {
		for _, f := range []struct {
			name string
			v    uint32
		}{
			{"HActive", t.HActive},
			{"HFrontPorch", t.HFrontPorch},
			{"HSync", t.HSync},
			{"HBackPorch", t.HBackPorch},
		} {
			if f.v%div != 0 {
				return fmt.Errorf("pv: %s of %d is not a multiple of %d pixels per clock",
					f.name, f.v, div)
			}
		}
	}

	return nil
}

// Registers returns the four timing registers for this mode, in the order
// HORZA, HORZB, VERTA, VERTB.
func (t *Timing) Registers() (horzA, horzB, vertA, vertB uint32) {
	div := t.perClock()

	horzA = (t.HBackPorch/div)<<16 | (t.HSync / div)
	horzB = (t.HFrontPorch/div)<<16 | (t.HActive / div)
	vertA = t.VBackPorch<<16 | t.VSync
	vertB = t.VFrontPorch<<16 | t.VActive

	return
}

// Decode reverses Registers, given the pixels-per-clock the path uses.
//
// The divisor has to be supplied because it is not recoverable from the
// registers: halved horizontal values are indistinguishable from a mode that is
// genuinely half as wide. That ambiguity is the reason a captured PV dump cannot
// be read without knowing which output it was driving.
func Decode(horzA, horzB, vertA, vertB uint32, pixelsPerClock uint32) Timing {
	if pixelsPerClock == 0 {
		pixelsPerClock = 1
	}

	return Timing{
		HBackPorch:     (horzA >> 16) * pixelsPerClock,
		HSync:          (horzA & 0xffff) * pixelsPerClock,
		HFrontPorch:    (horzB >> 16) * pixelsPerClock,
		HActive:        (horzB & 0xffff) * pixelsPerClock,
		VBackPorch:     vertA >> 16,
		VSync:          vertA & 0xffff,
		VFrontPorch:    vertB >> 16,
		VActive:        vertB & 0xffff,
		PixelsPerClock: pixelsPerClock,
	}
}

// DecodeControl breaks out the PV_CONTROL fields that describe how the
// pixelvalve is configured, as opposed to what it is timing.
type Control struct {
	Enabled       bool
	Format        uint32
	FIFOLevel     uint32 // the PV5 field, both halves combined
	PixelRep      uint32
	ClkSelect     uint32
	WaitHStart    bool
	ClrAtStart    bool
	TrigUnderflow bool
}

// DecodeControl decodes PV_CONTROL.
//
// FIFOLevel combines the low field with the PV5 high extension. Reading only the
// low six bits — which is what the unprefixed vc4 layout gives — silently loses
// the top two bits of the level on this SoC.
func DecodeControl(raw uint32) Control {
	return Control{
		Enabled:       raw&ControlEN != 0,
		Format:        (raw >> ControlFormatShift) & ControlFormatMask,
		FIFOLevel:     (raw>>ControlFIFOLevelShift)&ControlFIFOLevelMask | ((raw>>ControlFIFOLevelHighShift)&ControlFIFOLevelHighMask)<<6,
		PixelRep:      (raw >> ControlPixelRepShift) & ControlPixelRepMask,
		ClkSelect:     (raw >> ControlClkSelectShift) & ControlClkSelectMask,
		WaitHStart:    raw&ControlWaitHStart != 0,
		ClrAtStart:    raw&ControlClrAtStart != 0,
		TrigUnderflow: raw&ControlTriggerUnderflow != 0,
	}
}
