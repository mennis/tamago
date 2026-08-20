// BCM2711 PixelValve timing
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pv

import "testing"

// pixelvalve2, read out of a Raspberry Pi 4 Model B on 2026-07-31 while Linux
// was driving a 480x800 panel over HDMI0.
const (
	goldenCONTROL  = 0x06177001
	goldenVCONTROL = 0x00000003
	goldenHORZA    = 0x00140018
	goldenHORZB    = 0x001400f0
	goldenVERTA    = 0x001d0003
	goldenVERTB    = 0x000d0320
)

// The panel's mode, measured independently of any register — kmsprint under
// Linux reported:
//
//	480x800@62.29  32.000MHz  480/40/48/40/-  800/13/3/29/-
//
// None of these numbers were read out of the capture. That is the entire point:
// the test asks whether the mode, encoded, equals what the hardware held.
var panel = Timing{
	HActive: 480, HFrontPorch: 40, HSync: 48, HBackPorch: 40,
	VActive: 800, VFrontPorch: 13, VSync: 3, VBackPorch: 29,
	PixelClock:     32_000_000,
	PixelsPerClock: HDMIPixelsPerClock,
}

func TestRegistersMatchSilicon(t *testing.T) {
	horzA, horzB, vertA, vertB := panel.Registers()

	for _, tc := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"HORZA", horzA, goldenHORZA},
		{"HORZB", horzB, goldenHORZB},
		{"VERTA", vertA, goldenVERTA},
		{"VERTB", vertB, goldenVERTB},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %#08x, silicon has %#08x", tc.name, tc.got, tc.want)
		}
	}
}

// The horizontal intervals are halved and the vertical ones are not. That
// asymmetry is the single most surprising thing about this block, it is measured
// rather than documented, and a driver that "fixed" it by dividing both would
// produce a picture a third of the right height with no error anywhere.
func TestOnlyHorizontalIsDivided(t *testing.T) {
	horzA, horzB, vertA, vertB := panel.Registers()

	if got := horzB & 0xffff; got != panel.HActive/HDMIPixelsPerClock {
		t.Errorf("HORZB HACTIVE = %d, want %d (halved)", got, panel.HActive/2)
	}

	if got := vertB & 0xffff; got != panel.VActive {
		t.Errorf("VERTB VACTIVE = %d, want %d (NOT halved)", got, panel.VActive)
	}

	if got := horzA & 0xffff; got != panel.HSync/HDMIPixelsPerClock {
		t.Errorf("HORZA HSYNC = %d, want %d (halved)", got, panel.HSync/2)
	}

	if got := vertA & 0xffff; got != panel.VSync {
		t.Errorf("VERTA VSYNC = %d, want %d (NOT halved)", got, panel.VSync)
	}
}

// An independent check on the whole decode: the raster totals and the dot clock
// have to produce the refresh the mode line states. Nothing in Registers was
// fitted to this, and it touches no register and no PixelsPerClock, so agreement
// is a separate line of evidence rather than a restatement.
//
// Its limit, stated so nobody credits it with more: the totals are SUMS, so this
// is blind to a permutation. Swap the front and back porches and the refresh is
// identical while the raster is wrong. What catches ordering is
// TestRegistersMatchSilicon, which pins each interval to its own field.
func TestRefreshMatchesTheModeLine(t *testing.T) {
	if got, want := panel.HTotal(), uint32(608); got != want {
		t.Errorf("HTotal = %d, want %d", got, want)
	}

	if got, want := panel.VTotal(), uint32(845); got != want {
		t.Errorf("VTotal = %d, want %d", got, want)
	}

	// 62.29Hz to the hundredth, in millihertz.
	if got := panel.Refresh(); got < 62_280 || got > 62_300 {
		t.Errorf("Refresh = %d mHz, the mode line says 62.29Hz", got)
	}
}

// Decode is the inverse of Registers over a real mode.
func TestDecodeRoundTrip(t *testing.T) {
	got := Decode(goldenHORZA, goldenHORZB, goldenVERTA, goldenVERTB, HDMIPixelsPerClock)

	if got.HActive != panel.HActive || got.HFrontPorch != panel.HFrontPorch ||
		got.HSync != panel.HSync || got.HBackPorch != panel.HBackPorch {
		t.Errorf("horizontal decoded as %d/%d/%d/%d, want %d/%d/%d/%d",
			got.HActive, got.HFrontPorch, got.HSync, got.HBackPorch,
			panel.HActive, panel.HFrontPorch, panel.HSync, panel.HBackPorch)
	}

	if got.VActive != panel.VActive || got.VFrontPorch != panel.VFrontPorch ||
		got.VSync != panel.VSync || got.VBackPorch != panel.VBackPorch {
		t.Errorf("vertical decoded as %d/%d/%d/%d, want %d/%d/%d/%d",
			got.VActive, got.VFrontPorch, got.VSync, got.VBackPorch,
			panel.VActive, panel.VFrontPorch, panel.VSync, panel.VBackPorch)
	}
}

// Decoding with the wrong pixels-per-clock yields a mode half as wide, and the
// registers cannot tell you which is right. Pinned because it is the trap in
// reading any captured PV dump.
func TestDecodeCannotRecoverThePixelRate(t *testing.T) {
	wrong := Decode(goldenHORZA, goldenHORZB, goldenVERTA, goldenVERTB, 1)

	if wrong.HActive != 240 {
		t.Errorf("decoded HActive = %d, want 240 — the halved value taken at face value",
			wrong.HActive)
	}

	if wrong.VActive != panel.VActive {
		t.Error("vertical should be unaffected by the pixels-per-clock assumption")
	}
}

// An interval that does not fit its field truncates rather than faulting, and an
// odd interval on a two-pixel-per-clock path cannot be expressed at all. Both are
// refused, because the symptom of either is a subtly wrong raster that can only
// be diagnosed by photographing the panel.
func TestValidRefusesWhatCannotBeProgrammed(t *testing.T) {
	if err := panel.Valid(); err != nil {
		t.Fatalf("the panel's own mode was refused: %v", err)
	}

	odd := panel
	odd.HSync = 47 // not a multiple of 2

	if err := odd.Valid(); err == nil {
		t.Error("an odd horizontal interval on a 2-pixel-per-clock path was accepted")
	}

	wide := panel
	wide.HActive = 0x20000 // overflows the 16-bit field even after halving

	if err := wide.Valid(); err == nil {
		t.Error("an interval too wide for its field was accepted")
	}

	blank := panel
	blank.VActive = 0

	if err := blank.Valid(); err == nil {
		t.Error("a mode with no active area was accepted")
	}
}

// PV_CONTROL from the capture, decoded. FORMAT 0 is 24bpp, which is what an HDMI
// path should be; the pixelvalve was enabled; and PIXEL_REP is 0 — which is the
// finding, because it means the halved horizontal timing does NOT come from the
// field whose name suggests it.
func TestDecodeControlOnTheLivePixelvalve(t *testing.T) {
	c := DecodeControl(goldenCONTROL)

	if !c.Enabled {
		t.Error("the live pixelvalve decoded as disabled")
	}

	if c.Format != Format24BPP {
		t.Errorf("FORMAT = %d, want %d (24bpp) on an HDMI path", c.Format, Format24BPP)
	}

	if c.PixelRep != 0 {
		t.Errorf("PIXEL_REP = %d; if this is ever non-zero the halving story"+
			" in HDMIPixelsPerClock needs revisiting", c.PixelRep)
	}

	// Low field 46, high extension 3 -> 3<<6 | 46.
	if got, want := c.FIFOLevel, uint32(3<<6|46); got != want {
		t.Errorf("FIFOLevel = %d, want %d — the PV5 high bits are being dropped",
			got, want)
	}
}

// V_CONTROL says a continuous, progressive, video-enabled raster: no interlace,
// no DSI command mode. Worth pinning because interlace changes which of the EVEN
// timing registers matter, and this port ignores them.
func TestDecodeVControlIsProgressive(t *testing.T) {
	if goldenVCONTROL&VControlVIDEN == 0 {
		t.Error("VIDEN clear on a live pixelvalve")
	}

	if goldenVCONTROL&VControlContinuous == 0 {
		t.Error("CONTINUOUS clear: this port assumes a continuous raster")
	}

	if goldenVCONTROL&VControlInterlace != 0 {
		t.Error("INTERLACE set: the EVEN timing registers would matter and are ignored")
	}

	if goldenVCONTROL&VControlDSI != 0 {
		t.Error("DSI set on what should be an HDMI path")
	}
}
