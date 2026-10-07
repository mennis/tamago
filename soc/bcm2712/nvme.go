// BCM2712 PCIe NVMe support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"errors"
	"fmt"

	"github.com/usbarmory/tamago/dma"
	"github.com/usbarmory/tamago/soc/bcm2712/pcie"
	"github.com/usbarmory/tamago/soc/nvme"
)

const (
	pcie1RCBase     = 0x10_00110000
	pcie1CPUBase    = 0x1b_00000000
	pcie1PCIMemBase = 0x00_00000000
	pcie1MemSize    = 0x1_00000000

	pcieRescalBase = 0x10_00119500
	resetBase      = 0x10_01504318
	pcie1BridgeID  = 43
	pcie1DMAOffset = 0x10_00000000
)

var pcie1InboundWindows = []pcie.InboundWindow{
	{Size: 0x10_00000000, PCIOffset: pcie1DMAOffset, CPUAddr: 0},
	{Size: 0x00001000, PCIOffset: 0xff_fffff000, CPUAddr: 0x10_00131000},
}

type nvmeRegIO struct{}

func (nvmeRegIO) Read32(addr uint64) uint32       { return Read32(addr) }
func (nvmeRegIO) Write32(addr uint64, val uint32) { Write32(addr, val) }

type nvmeDMA struct {
	region *dma.Region
}

func (d *nvmeDMA) Reserve(size, align int) (uint, []byte) {
	return d.region.Reserve(size, align)
}
func (d *nvmeDMA) Release(addr uint)      { d.region.Release(addr) }
func (d *nvmeDMA) Bus(addr uint64) uint64 { return pcie1DMAOffset + addr }
func (d *nvmeDMA) Clean(addr uint, size int) {
	ARM.CleanDataCacheRange(uintptr(addr), uintptr(size))
}
func (d *nvmeDMA) Invalidate(addr uint, size int) {
	ARM.InvalidateDataCacheRange(uintptr(addr), uintptr(size))
}
func (d *nvmeDMA) Barrier() { ARM.DataSynchronizationBarrier() }

var (
	pcie1Host *pcie.Host

	// NVMe is namespace 1 of the controller attached to the Pi 5 external
	// PCIe connector. It is set by InitNVMe.
	NVMe *nvme.Namespace
)

// InitNVMe brings up the Pi 5 external PCIe x1 root port, maps an NVMe BAR0,
// enables DMA, and initializes namespace 1 using polled queues. It must run
// after Init and requires a DMA region in ordinary system RAM below 64 GiB.
func InitNVMe(region *dma.Region) error {
	if NVMe != nil {
		return nil
	}
	if region == nil {
		return errors.New("bcm2712: NVMe DMA region required")
	}
	if uint64(region.End()) > 0x10_00000000 {
		return errors.New("bcm2712: NVMe DMA region exceeds PCIe1 inbound window")
	}

	bus := nvmeRegIO{}
	if pcie1Host == nil {
		reset := &pcie.ResetControl{
			IO: bus, Delay: rp1Delay, RescalBase: pcieRescalBase,
			SWInitBase: resetBase, BridgeID: pcie1BridgeID,
		}
		pcie1Host = pcie.NewHost(bus, rp1Delay, ARM.DataSynchronizationBarrier, pcie.HostConfig{
			RCBase: pcie1RCBase, CPUBase: pcie1CPUBase,
			PCIMemBase: pcie1PCIMemBase, MemSize: pcie1MemSize,
			Inbound: pcie1InboundWindows,
			Rescal:  reset.Rescal, BridgeReset: reset.BridgeReset,
		})
	}
	dev, err := pcie1Host.Init()
	if err != nil {
		return fmt.Errorf("bcm2712: PCIe1 bring-up: %w", err)
	}
	if !dev.IsNVMe() {
		return fmt.Errorf("bcm2712: PCIe1 endpoint class %#06x is not NVMe", dev.Class())
	}
	bar, err := dev.MapBAR(0, 0)
	if err != nil {
		return fmt.Errorf("bcm2712: map NVMe BAR0: %w", err)
	}
	if err := dev.EnableDMA(); err != nil {
		return fmt.Errorf("bcm2712: enable NVMe DMA: %w", err)
	}

	controller := &nvme.Controller{
		Base: bar.CPUAddr, Reg: bus, DMA: &nvmeDMA{region: region}, Sleep: rp1Delay,
	}
	NVMe, err = controller.Init()
	if err != nil {
		NVMe = nil
		return fmt.Errorf("bcm2712: initialize NVMe controller: %w", err)
	}
	return nil
}
