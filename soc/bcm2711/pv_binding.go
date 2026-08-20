// BCM2711 PixelValve register binding
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"fmt"

	"github.com/usbarmory/tamago/soc/bcm2711/pv"
)

// PixelValves indexes the five pixelvalves by the offsets in bcm2711.go.
//
// Which one drives what is NOT guessable from the numbering and was established
// by reading them on silicon: PV2 carries HDMI0, PV3 the VEC, PV4 HDMI1. A
// register dump taken while a panel was live found PV2 timing the panel's mode
// and every other pixelvalve at reset values.
var PixelValves = [5]uint64{
	PV0Offset,
	PV1Offset,
	PV2Offset,
	PV3Offset,
	PV4Offset,
}

// PVHDMI0 is the pixelvalve that drives HDMI0, which is the connector the bench
// panel is on and the only one this port has exercised.
const PVHDMI0 = 2

// PVRead reads a pixelvalve register.
func PVRead(n int, offset uint64) (uint32, error) {
	if n < 0 || n >= len(PixelValves) {
		return 0, fmt.Errorf("bcm2711: pixelvalve %d out of range", n)
	}

	return Read32(PeripheralAddress(PixelValves[n] + offset)), nil
}

// PVWrite writes a pixelvalve register.
func PVWrite(n int, offset uint64, val uint32) error {
	if n < 0 || n >= len(PixelValves) {
		return fmt.Errorf("bcm2711: pixelvalve %d out of range", n)
	}

	Write32(PeripheralAddress(PixelValves[n]+offset), val)

	return nil
}

// PVState is a pixelvalve's complete WRITABLE configuration.
//
// Deliberately not everything the block exposes. INTSTAT and STAT are status,
// and FRAME_COUNT-style registers move on their own; writing a captured value
// back to those either does nothing or acknowledges an interrupt that has not
// happened. A restore has to put back exactly what was configured and touch
// nothing that describes what the hardware is currently doing — otherwise
// "restore" is itself a modification.
type PVState struct {
	Control    uint32
	VControl   uint32
	VSyncDEven uint32
	HorzA      uint32
	HorzB      uint32
	VertA      uint32
	VertB      uint32
	VertAEven  uint32
	VertBEven  uint32
	IntEn      uint32
	HActAct    uint32
}

// pvStateRegs maps each PVState field to its register offset, in the order a
// restore should write them. Kept as one table so capture and restore cannot
// drift apart — the failure mode of two hand-written lists is a register that is
// saved and never put back, which looks like a working restore until the one
// boot where that register mattered.
var pvStateRegs = []struct {
	offset uint64
	field  func(*PVState) *uint32
}{
	{pv.CONTROL, func(s *PVState) *uint32 { return &s.Control }},
	{pv.V_CONTROL, func(s *PVState) *uint32 { return &s.VControl }},
	{pv.VSYNCD_EVN, func(s *PVState) *uint32 { return &s.VSyncDEven }},
	{pv.HORZA, func(s *PVState) *uint32 { return &s.HorzA }},
	{pv.HORZB, func(s *PVState) *uint32 { return &s.HorzB }},
	{pv.VERTA, func(s *PVState) *uint32 { return &s.VertA }},
	{pv.VERTB, func(s *PVState) *uint32 { return &s.VertB }},
	{pv.VERTA_EVEN, func(s *PVState) *uint32 { return &s.VertAEven }},
	{pv.VERTB_EVEN, func(s *PVState) *uint32 { return &s.VertBEven }},
	{pv.INTEN, func(s *PVState) *uint32 { return &s.IntEn }},
	{pv.HACT_ACT, func(s *PVState) *uint32 { return &s.HActAct }},
}

// CapturePV reads a pixelvalve's writable configuration.
//
// Take one of these BEFORE changing anything. It is the only way back: the
// firmware's timing cannot be recomputed after the fact, because deriving it
// would need the mode, and the mode is exactly what a failed experiment has
// destroyed.
func CapturePV(n int) (s PVState, err error) {
	for _, r := range pvStateRegs {
		v, err := PVRead(n, r.offset)

		if err != nil {
			return s, err
		}

		*r.field(&s) = v
	}

	return
}

// RestorePV writes a captured configuration back.
//
// CONTROL goes last and is written twice — once with the enable bit cleared
// alongside everything else, then in full. Timing registers are latched while
// the valve runs, so putting the old intervals back underneath a running raster
// is a mode change performed one register at a time, with the display briefly
// timed by a mixture of two modes. Stopping first makes the restore atomic from
// the panel's point of view.
func RestorePV(n int, s PVState) error {
	if _, err := PVRead(n, pv.CONTROL); err != nil {
		return err
	}

	// Stop, preserving configuration, so the timing writes below land on a
	// valve that is not scanning.
	if err := PVWrite(n, pv.CONTROL, s.Control&^pv.ControlEN); err != nil {
		return err
	}

	for _, r := range pvStateRegs {
		if r.offset == pv.CONTROL {
			continue
		}

		if err := PVWrite(n, r.offset, *r.field(&s)); err != nil {
			return err
		}
	}

	return PVWrite(n, pv.CONTROL, s.Control)
}

// Timing decodes a pixelvalve's current mode.
//
// pixelsPerClock cannot be recovered from the registers (see pv.Decode), so the
// caller supplies it: pv.HDMIPixelsPerClock for PV2 and PV4, 1 elsewhere.
func (s PVState) Timing(pixelsPerClock uint32) pv.Timing {
	return pv.Decode(s.HorzA, s.HorzB, s.VertA, s.VertB, pixelsPerClock)
}

// DecodeControl decodes the pixelvalve's configuration word.
func (s PVState) DecodeControl() pv.Control {
	return pv.DecodeControl(s.Control)
}

// SetPVTiming reprograms a pixelvalve's timing, returning the previous state so
// the caller can put it back.
//
// Wrong intervals produce a blank panel and no error, so the caller gets the old
// state back as a value rather than being trusted to have saved one, and the
// timing is validated before any register is touched: a mode that cannot be
// expressed is refused rather than truncated.
//
// It does not touch the pixel clock. The intervals are counted against whatever
// clock the firmware established, so this reshapes a raster at a fixed dot clock
// and nothing more.
func SetPVTiming(n int, t *pv.Timing) (previous PVState, err error) {
	if err = t.Valid(); err != nil {
		return previous, err
	}

	if previous, err = CapturePV(n); err != nil {
		return previous, err
	}

	horzA, horzB, vertA, vertB := t.Registers()

	next := previous
	next.HorzA, next.HorzB, next.VertA, next.VertB = horzA, horzB, vertA, vertB

	if err = RestorePV(n, next); err != nil {
		return previous, err
	}

	return previous, nil
}
