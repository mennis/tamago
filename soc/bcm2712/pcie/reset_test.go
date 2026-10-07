// BCM2712 PCIe reset-control tests
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

import (
	"testing"
	"time"
)

type resetModel struct {
	regs   map[uint64]uint32
	writes []struct {
		addr uint64
		val  uint32
	}
}

func (m *resetModel) Read32(addr uint64) uint32 { return m.regs[addr] }
func (m *resetModel) Write32(addr uint64, val uint32) {
	m.regs[addr] = val
	m.writes = append(m.writes, struct {
		addr uint64
		val  uint32
	}{addr, val})
}

func TestBCM2712ResetControl(t *testing.T) {
	const (
		rescal = uint64(0x10_00119500)
		swinit = uint64(0x10_01504318)
	)
	m := &resetModel{regs: map[uint64]uint32{rescal + 8: 1}}
	r := &ResetControl{IO: m, Delay: func(time.Duration) {}, RescalBase: rescal, SWInitBase: swinit, BridgeID: 43}

	if err := r.BridgeReset(true); err != nil {
		t.Fatalf("ResetControl.BridgeReset(true) error = %v", err)
	}
	if err := r.Rescal(); err != nil {
		t.Fatalf("ResetControl.Rescal() error = %v", err)
	}
	if err := r.BridgeReset(false); err != nil {
		t.Fatalf("ResetControl.BridgeReset(false) error = %v", err)
	}

	want := []struct {
		addr uint64
		val  uint32
	}{
		{swinit + 0x18, 1 << 11},
		{rescal, 1},
		{rescal, 0},
		{swinit + 0x18 + 4, 1 << 11},
	}
	if len(m.writes) != len(want) {
		t.Fatalf("reset write count = %d, want %d (%v)", len(m.writes), len(want), m.writes)
	}
	for i := range want {
		if m.writes[i] != want[i] {
			t.Errorf("reset write %d = %#v, want %#v", i, m.writes[i], want[i])
		}
	}
}

func TestRescalTimeout(t *testing.T) {
	m := &resetModel{regs: make(map[uint64]uint32)}
	r := &ResetControl{IO: m, Delay: func(time.Duration) {}, RescalBase: 0x1000}
	if err := r.Rescal(); err == nil {
		t.Error("ResetControl.Rescal() succeeded without status")
	}
}
