// BCM2712 generic PCIe root-complex bring-up
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

import (
	"errors"
	"fmt"
	"time"
)

const (
	cfgClassRevision = 0x08
	cfgLinkControl2  = 0xdc // PCIe capability at 0xac + PCI_EXP_LNKCTL2

	classNVMe     = 0x010802
	linkSpeedMask = 0xf
)

// HostConfig describes one BCM2712 root complex and its address translations.
// Rescal and BridgeReset are board hooks because those controls live outside
// the root-complex register block.
type HostConfig struct {
	RCBase     uint64
	CPUBase    uint64
	PCIMemBase uint64
	MemSize    uint64
	Inbound    []InboundWindow

	Rescal      func() error
	BridgeReset func(assert bool) error
}

// Host controls a generic, single-endpoint BCM2712 PCIe root complex.
type Host struct {
	cfg    HostConfig
	rc     *Controller
	device *Device
}

// Device is the endpoint at bus 1, device 0, function 0.
type Device struct {
	host          *Host
	VendorDevice  uint32
	ClassRevision uint32
	mappedBARs    int
}

// BAR describes one endpoint memory BAR mapped into the host CPU aperture.
type BAR struct {
	Index        int
	PCIAddr      uint64
	CPUAddr      uint64
	Size         uint64
	Is64Bit      bool
	Prefetchable bool
}

// Class returns the endpoint's 24-bit PCI class/subclass/programming-interface
// tuple.
func (d *Device) Class() uint32 { return d.ClassRevision >> 8 }

// IsNVMe reports whether the endpoint implements the NVMe programming
// interface.
func (d *Device) IsNVMe() bool { return d.Class() == classNVMe }

// ReadConfig reads one aligned configuration-space dword.
func (d *Device) ReadConfig(off uint64) (uint32, error) {
	return d.host.rc.epRead(off)
}

// WriteConfig writes one aligned configuration-space dword.
func (d *Device) WriteConfig(off uint64, value uint32) error {
	return d.host.rc.epWrite(off, value)
}

// MapBAR sizes one memory BAR and assigns it a PCI address in the host's
// outbound aperture. pciAddr must satisfy the BAR's alignment requirement.
func (d *Device) MapBAR(index int, pciAddr uint64) (bar BAR, retErr error) {
	if index < 0 || index >= 6 {
		return BAR{}, fmt.Errorf("pcie: BAR index %d out of range", index)
	}
	off := uint64(cfgBAR0 + 4*index)
	command, err := d.ReadConfig(cfgCommand)
	if err != nil {
		return BAR{}, err
	}
	if err := d.WriteConfig(cfgCommand, command&^cmdMemory&cmdLowMask); err != nil {
		return BAR{}, err
	}
	defer func() {
		if err := d.WriteConfig(cfgCommand, command&cmdLowMask); err != nil && retErr == nil {
			retErr = fmt.Errorf("pcie: restore PCI command after BAR sizing: %w", err)
		}
	}()

	origLo, err := d.ReadConfig(off)
	if err != nil {
		return BAR{}, err
	}
	if origLo&barIOSpace != 0 {
		return BAR{}, fmt.Errorf("pcie: BAR %d is I/O space", index)
	}
	is64 := origLo&barTypeMask == barType64
	if origLo&barTypeMask != 0 && !is64 {
		return BAR{}, fmt.Errorf("pcie: BAR %d has reserved memory type", index)
	}
	if is64 && index == 5 {
		return BAR{}, errors.New("pcie: 64-bit BAR cannot start at index 5")
	}

	var origHi uint32
	if is64 {
		origHi, err = d.ReadConfig(off + 4)
		if err != nil {
			return BAR{}, err
		}
	}
	if err := d.WriteConfig(off, 0xffffffff); err != nil {
		return BAR{}, err
	}
	if is64 {
		if err := d.WriteConfig(off+4, 0xffffffff); err != nil {
			return BAR{}, err
		}
	}
	maskLo, err := d.ReadConfig(off)
	if err != nil {
		return BAR{}, err
	}
	var maskHi uint32
	if is64 {
		maskHi, err = d.ReadConfig(off + 4)
		if err != nil {
			return BAR{}, err
		}
		if err := d.WriteConfig(off+4, origHi); err != nil {
			return BAR{}, err
		}
	}
	if err := d.WriteConfig(off, origLo); err != nil {
		return BAR{}, err
	}

	mask := uint64(maskLo &^ 0xf)
	if is64 {
		mask |= uint64(maskHi) << 32
	} else {
		mask |= 0xffffffff00000000
	}
	size := ^mask + 1
	if size == 0 || size&(size-1) != 0 {
		return BAR{}, fmt.Errorf("pcie: BAR %d returned invalid size mask %#x", index, mask)
	}
	if pciAddr&(size-1) != 0 {
		return BAR{}, fmt.Errorf("pcie: BAR %d address %#x is not aligned to size %#x", index, pciAddr, size)
	}
	windowEnd := d.host.cfg.PCIMemBase + d.host.cfg.MemSize
	if windowEnd < d.host.cfg.PCIMemBase || pciAddr < d.host.cfg.PCIMemBase || pciAddr >= windowEnd || size > windowEnd-pciAddr {
		return BAR{}, fmt.Errorf("pcie: BAR %d range [%#x,%#x) is outside host aperture", index, pciAddr, pciAddr+size)
	}

	if err := d.WriteConfig(off, uint32(pciAddr)); err != nil {
		return BAR{}, err
	}
	if is64 {
		if err := d.WriteConfig(off+4, uint32(pciAddr>>32)); err != nil {
			return BAR{}, err
		}
	}
	d.mappedBARs++
	return BAR{
		Index: index, PCIAddr: pciAddr,
		CPUAddr: d.host.cfg.CPUBase + pciAddr - d.host.cfg.PCIMemBase,
		Size:    size, Is64Bit: is64, Prefetchable: origLo&barPrefetch != 0,
	}, nil
}

// EnableDMA enables endpoint and root-port memory decoding and bus mastering.
// It is valid only after at least one BAR and an inbound DMA window exist.
func (d *Device) EnableDMA() error {
	if d.mappedBARs == 0 {
		return errors.New("pcie: cannot enable device before mapping a BAR")
	}
	if len(d.host.cfg.Inbound) == 0 {
		return errors.New("pcie: cannot enable DMA without an inbound window")
	}
	command, err := d.ReadConfig(cfgCommand)
	if err != nil {
		return err
	}
	if err := d.WriteConfig(cfgCommand, (command|cmdMemory|cmdBusMaster)&cmdLowMask); err != nil {
		return err
	}
	rootCommand := (d.host.rc.rd(cfgCommand) | cmdMemory | cmdBusMaster) & cmdLowMask
	d.host.rc.wr(cfgCommand, rootCommand)
	_ = d.host.rc.rd(cfgCommand)
	d.host.rc.barrier()
	return nil
}

// NewHost returns a generic BCM2712 PCIe host controller.
func NewHost(io RegIO, sleep func(time.Duration), barrier func(), cfg HostConfig) *Host {
	rc := New(io, sleep, barrier, cfg.RCBase, cfg.CPUBase)
	rc.EnableDMA(cfg.Inbound)
	return &Host{cfg: cfg, rc: rc}
}

func (h *Host) validate() error {
	switch {
	case h.cfg.RCBase == 0:
		return errors.New("pcie: host RC base is zero")
	case h.cfg.CPUBase&(1<<20-1) != 0:
		return errors.New("pcie: host CPU window is not MiB aligned")
	case h.cfg.PCIMemBase&(1<<20-1) != 0:
		return errors.New("pcie: host PCI window is not MiB aligned")
	case h.cfg.MemSize == 0 || h.cfg.MemSize&(1<<20-1) != 0:
		return errors.New("pcie: host memory window size is not a non-zero MiB multiple")
	case h.cfg.CPUBase+h.cfg.MemSize < h.cfg.CPUBase:
		return errors.New("pcie: host CPU window overflows")
	case h.cfg.PCIMemBase+h.cfg.MemSize < h.cfg.PCIMemBase:
		return errors.New("pcie: host PCI window overflows")
	case h.cfg.PCIMemBase+h.cfg.MemSize-1 > 0xffffffff:
		return errors.New("pcie: non-prefetchable PCI window exceeds 32-bit Type-1 aperture")
	case h.cfg.Rescal == nil:
		return errors.New("pcie: host RESCAL hook is required")
	case h.cfg.BridgeReset == nil:
		return errors.New("pcie: host bridge-reset hook is required")
	}
	return nil
}

func (h *Host) programOutboundWindow() {
	last := h.cfg.CPUBase + h.cfg.MemSize - 1
	pciLast := h.cfg.PCIMemBase + h.cfg.MemSize - 1
	h.rc.wr(regWin0Lo, uint32(h.cfg.PCIMemBase))
	h.rc.wr(regWin0Hi, uint32(h.cfg.PCIMemBase>>32))
	h.rc.wr(regWin0BaseLim,
		(uint32(last>>20)&0xfff)<<20|(uint32(h.cfg.CPUBase>>20)&0xfff)<<4)
	h.rc.wr(regWin0BaseHi, uint32(h.cfg.CPUBase>>32))
	h.rc.wr(regWin0LimitHi, uint32(last>>32))
	h.rc.wr(cfgMemBaseLimit,
		(uint32(pciLast>>20)&0xfff)<<20|(uint32(h.cfg.PCIMemBase>>20)&0xfff)<<4)
}

// Init cold-starts the root complex and returns the single endpoint on bus 1.
// It establishes config-space reachability but leaves BAR assignment and DMA
// enablement to the caller.
func (h *Host) Init() (*Device, error) {
	if err := h.validate(); err != nil {
		return nil, err
	}
	if h.device != nil {
		if !h.rc.linkActive() {
			return nil, &Error{StageReady, "link not active", h.rc.rd(regPCIeStatus)}
		}
		return h.device, nil
	}
	// RESCAL is reached through PCIe controller 1's bridge. Ensure that bridge
	// is live before calibration; asserting it here can make the RESCAL access
	// hang the AXI fabric. Pulse the bridge reset only after calibration.
	if err := h.cfg.BridgeReset(false); err != nil {
		return nil, fmt.Errorf("pcie: deassert bridge for RESCAL: %w", err)
	}
	if err := h.cfg.Rescal(); err != nil {
		return nil, fmt.Errorf("pcie: RESCAL: %w", err)
	}
	if err := h.cfg.BridgeReset(true); err != nil {
		return nil, fmt.Errorf("pcie: assert bridge reset: %w", err)
	}
	h.rc.sleep(100 * time.Microsecond)
	if err := h.cfg.BridgeReset(false); err != nil {
		return nil, fmt.Errorf("pcie: deassert bridge reset: %w", err)
	}

	c := h.rc
	c.assertPERST()
	c.wr(regHardDebug, c.rd(regHardDebug)&^serdesIDDQ)
	c.sleep(perstSettle)
	c.wr(regMiscCtrl, (c.rd(regMiscCtrl)&^miscCtrlMask)|miscCtrlConfig)
	c.wr(regPriv1IDVal3, classBridge)

	// Request Gen 2 while preserving the port's lane-width capability.
	c.wr(regPriv1LinkCap, (c.rd(regPriv1LinkCap)&^linkSpeedMask)|2)
	c.wr(cfgLinkControl2, (c.rd(cfgLinkControl2)&^linkSpeedMask)|2)

	h.programOutboundWindow()
	if err := c.programInboundWindows(); err != nil {
		return nil, err
	}
	c.rmw(regUbusCtrl, ubusErrSuppr)
	c.wr(regAxiReadErr, axiReadErrVal)
	c.wr(regUbusTimeout, ubusTimeoutVal)
	c.wr(regCfgRetryTO, cfgRetryTOVal)
	if err := c.phySetup(); err != nil {
		return nil, err
	}

	c.barrier()
	c.deassertPERST()
	c.sleep(linkWait)
	var status uint32
	for i := 0; i < linkMaxPolls; i++ {
		status = c.rd(regPCIeStatus)
		if status&statusLinkReady == statusLinkReady {
			break
		}
		c.sleep(pollInterval)
	}
	if status&statusLinkReady != statusLinkReady {
		return nil, &Error{StageLinkTimeout, "link did not train (PHYLINKUP|DL_ACTIVE)", status}
	}
	if status&statusPort == 0 {
		return nil, &Error{StagePortCheck, "not in root-complex mode after link-up", status}
	}

	c.wr(cfgBusNumbers, busPri0Sec1)
	var vendorDevice uint32
	var err error
	for i := 0; i < cfgMaxPolls; i++ {
		vendorDevice, err = c.epRead(cfgVendorDevice)
		if err != nil {
			return nil, err
		}
		if vendorDevice != 0 && vendorDevice != 0xffffffff {
			break
		}
		c.sleep(cfgPollWait)
	}
	if vendorDevice == 0 || vendorDevice == 0xffffffff {
		return nil, &Error{StageEndpointNotFound, "no endpoint on bus 1", vendorDevice}
	}
	classRevision, err := c.epRead(cfgClassRevision)
	if err != nil {
		return nil, err
	}
	h.device = &Device{host: h, VendorDevice: vendorDevice, ClassRevision: classRevision}
	return h.device, nil
}
