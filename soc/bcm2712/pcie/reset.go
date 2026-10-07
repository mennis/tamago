// BCM2712 PCIe reset controls
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

import (
	"errors"
	"time"
)

const (
	rescalStart  = 0x0
	rescalStatus = 0x8
	rescalBit    = 1

	swInitSet      = 0x0
	swInitClear    = 0x4
	swInitBankSize = 0x18
)

// ResetControl adapts the BCM2712 RESCAL and SW_INIT register blocks to the
// hooks required by HostConfig.
type ResetControl struct {
	IO         RegIO
	Delay      func(time.Duration)
	RescalBase uint64
	SWInitBase uint64
	BridgeID   uint
}

// Rescal starts PCIe/SATA resistor calibration and waits at most 1 ms for it.
func (r *ResetControl) Rescal() error {
	if r.IO == nil || r.RescalBase == 0 {
		return errors.New("pcie: RESCAL MMIO is not configured")
	}
	delay := r.Delay
	if delay == nil {
		delay = time.Sleep
	}
	start := r.IO.Read32(r.RescalBase + rescalStart)
	r.IO.Write32(r.RescalBase+rescalStart, start|rescalBit)
	if r.IO.Read32(r.RescalBase+rescalStart)&rescalBit == 0 {
		return errors.New("pcie: RESCAL start bit did not latch")
	}
	for i := 0; i < 10; i++ {
		if r.IO.Read32(r.RescalBase+rescalStatus)&rescalBit != 0 {
			r.IO.Write32(r.RescalBase+rescalStart, start&^rescalBit)
			return nil
		}
		delay(100 * time.Microsecond)
	}
	return errors.New("pcie: RESCAL timeout")
}

// BridgeReset asserts or deasserts the configured SW_INIT reset line.
func (r *ResetControl) BridgeReset(assert bool) error {
	if r.IO == nil || r.SWInitBase == 0 {
		return errors.New("pcie: bridge-reset MMIO is not configured")
	}
	if r.BridgeID >= 64 {
		return errors.New("pcie: bridge-reset ID is out of range")
	}
	bank := uint64(r.BridgeID/32) * swInitBankSize
	bit := uint32(1) << (r.BridgeID & 31)
	off := uint64(swInitSet)
	if !assert {
		off = swInitClear
	}
	r.IO.Write32(r.SWInitBase+bank+off, bit)
	if !assert {
		delay := r.Delay
		if delay == nil {
			delay = time.Sleep
		}
		delay(100 * time.Microsecond)
	}
	return nil
}
