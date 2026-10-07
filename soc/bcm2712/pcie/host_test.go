// BCM2712 generic PCIe host-controller tests
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

import (
	"reflect"
	"testing"
	"time"
)

type hostModel struct {
	rcBase       uint64
	regs         map[uint64]uint32
	events       []string
	barSizing    bool
	vendorMisses int
}

func newHostModel(rcBase uint64) *hostModel {
	m := &hostModel{rcBase: rcBase, regs: make(map[uint64]uint32)}
	m.regs[rcBase+regPCIeStatus] = statusReady
	m.regs[rcBase+cfgVendorDevice] = 0x271214e4
	m.regs[rcBase+regExtCfgData+cfgVendorDevice] = 0x12348086
	m.regs[rcBase+regExtCfgData+cfgClassRevision] = classNVMe << 8
	m.regs[rcBase+regExtCfgData+cfgBAR0] = barType64
	m.regs[rcBase+regExtCfgData+cfgBAR1] = 0
	return m
}

func (m *hostModel) Read32(addr uint64) uint32 {
	if addr == m.rcBase+regExtCfgData+cfgVendorDevice && m.vendorMisses > 0 {
		m.vendorMisses--
		return 0xffffffff
	}
	if m.barSizing {
		switch addr {
		case m.rcBase + regExtCfgData + cfgBAR0:
			return 0xffffc000 | barType64
		case m.rcBase + regExtCfgData + cfgBAR1:
			return 0xffffffff
		}
	}
	return m.regs[addr]
}

func (m *hostModel) Write32(addr uint64, value uint32) {
	if addr&0xffff == regMDIOWrData {
		value &^= mdioDataDone
	}
	if addr == m.rcBase+regExtCfgData+cfgBAR0 && value == 0xffffffff {
		m.barSizing = true
	}
	if addr == m.rcBase+regExtCfgData+cfgBAR1 && m.barSizing && value != 0xffffffff {
		m.barSizing = false
	}
	m.regs[addr] = value
}

func TestHostColdStartAndEnumerateNVMe(t *testing.T) {
	const (
		rcBase  = uint64(0x10_00110000)
		cpuBase = uint64(0x1b_00000000)
	)

	m := newHostModel(rcBase)
	h := NewHost(m, func(time.Duration) {}, func() {}, HostConfig{
		RCBase:     rcBase,
		CPUBase:    cpuBase,
		PCIMemBase: 0,
		MemSize:    1 << 32,
		Inbound: []InboundWindow{
			{Size: 0x10_00000000, PCIOffset: 0x10_00000000, CPUAddr: 0},
		},
		Rescal: func() error {
			m.events = append(m.events, "rescal")
			return nil
		},
		BridgeReset: func(assert bool) error {
			if assert {
				m.events = append(m.events, "bridge-assert")
			} else {
				m.events = append(m.events, "bridge-deassert")
			}
			return nil
		},
	})

	dev, err := h.Init()
	if err != nil {
		t.Fatalf("Host.Init() error = %v", err)
	}
	if got, want := m.events, []string{"bridge-deassert", "rescal", "bridge-assert", "bridge-deassert"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Host.Init() reset events = %v, want %v", got, want)
	}
	if got, want := dev.VendorDevice, uint32(0x12348086); got != want {
		t.Errorf("Device.VendorDevice = %#08x, want %#08x", got, want)
	}
	if got, want := dev.Class(), uint32(classNVMe); got != want {
		t.Errorf("Device.Class() = %#06x, want %#06x", got, want)
	}
	if got, want := m.regs[rcBase+cfgMemBaseLimit], uint32(0xfff00000); got != want {
		t.Errorf("root memory base/limit = %#08x, want %#08x", got, want)
	}
	if got := m.regs[rcBase+cfgLinkControl2] & linkSpeedMask; got != 2 {
		t.Errorf("root Link Control 2 target speed = %d, want Gen 2", got)
	}
}

func TestHostWaitsForEndpointConfigSpace(t *testing.T) {
	const rcBase = uint64(0x10_00110000)
	m := newHostModel(rcBase)
	m.vendorMisses = 3
	h := NewHost(m, func(time.Duration) {}, func() {}, HostConfig{
		RCBase: rcBase, CPUBase: 0x1b_00000000, MemSize: 1 << 32,
		Rescal: func() error { return nil }, BridgeReset: func(bool) error { return nil },
	})
	if _, err := h.Init(); err != nil {
		t.Fatalf("Host.Init() error = %v", err)
	}
	if m.vendorMisses != 0 {
		t.Errorf("Host.Init() did not wait for endpoint, misses remaining = %d", m.vendorMisses)
	}
}

func TestHostRejectsOverflowingOutboundWindow(t *testing.T) {
	h := NewHost(newHostModel(1), func(time.Duration) {}, func() {}, HostConfig{
		RCBase: 1, CPUBase: ^uint64(0) &^ (1<<20 - 1), MemSize: 2 << 20,
		Rescal: func() error { return nil }, BridgeReset: func(bool) error { return nil },
	})
	if _, err := h.Init(); err == nil {
		t.Fatal("Host.Init() accepted an overflowing CPU window")
	}
}

func TestDeviceMap64BitBARAndEnableDMA(t *testing.T) {
	const (
		rcBase  = uint64(0x10_00110000)
		cpuBase = uint64(0x1b_00000000)
	)

	m := newHostModel(rcBase)
	h := NewHost(m, func(time.Duration) {}, func() {}, HostConfig{
		RCBase:     rcBase,
		CPUBase:    cpuBase,
		PCIMemBase: 0,
		MemSize:    1 << 32,
		Inbound: []InboundWindow{
			{Size: 0x10_00000000, PCIOffset: 0x10_00000000, CPUAddr: 0},
		},
		Rescal:      func() error { return nil },
		BridgeReset: func(bool) error { return nil },
	})
	dev, err := h.Init()
	if err != nil {
		t.Fatalf("Host.Init() error = %v", err)
	}

	bar, err := dev.MapBAR(0, 0)
	if err != nil {
		t.Fatalf("Device.MapBAR() error = %v", err)
	}
	if got, want := bar.Size, uint64(16<<10); got != want {
		t.Errorf("BAR.Size = %#x, want %#x", got, want)
	}
	if got, want := bar.CPUAddr, cpuBase; got != want {
		t.Errorf("BAR.CPUAddr = %#x, want %#x", got, want)
	}
	if !bar.Is64Bit {
		t.Error("BAR.Is64Bit = false, want true")
	}
	if err := dev.EnableDMA(); err != nil {
		t.Fatalf("Device.EnableDMA() error = %v", err)
	}
	if got := m.regs[rcBase+regExtCfgData+cfgCommand]; got&(cmdMemory|cmdBusMaster) != cmdMemory|cmdBusMaster {
		t.Errorf("endpoint PCI_COMMAND = %#08x, want MSE|BME", got)
	}
	if got := m.regs[rcBase+cfgCommand]; got&(cmdMemory|cmdBusMaster) != cmdMemory|cmdBusMaster {
		t.Errorf("root PCI_COMMAND = %#08x, want MSE|BME", got)
	}
}

func TestHostAcceptsNonNVMeEndpoint(t *testing.T) {
	const rcBase = uint64(0x10_00110000)
	m := newHostModel(rcBase)
	m.regs[rcBase+regExtCfgData+cfgClassRevision] = 0x020000 << 8
	h := NewHost(m, func(time.Duration) {}, func() {}, HostConfig{
		RCBase: rcBase, CPUBase: 0x1b_00000000, MemSize: 1 << 32,
		Rescal: func() error { return nil }, BridgeReset: func(bool) error { return nil },
	})
	dev, err := h.Init()
	if err != nil {
		t.Fatalf("Host.Init() error = %v", err)
	}
	if dev.IsNVMe() {
		t.Error("Device.IsNVMe() = true for network-class endpoint")
	}
}
