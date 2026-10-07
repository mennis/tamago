// BCM2712 RP1 Ethernet support
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
	"github.com/usbarmory/tamago/phy/broadcom/bcm54213"
	"github.com/usbarmory/tamago/soc/bcm2712/gem"
)

// RP1 Cadence GEM (DT ethernet@100000, RP1 address 0x40100000)
const (
	gemBase = 0x100000

	// RP1_INT_ETH (DT-bindings/mfd/rp1.h)
	gemMSIVector = 6

	// PCIe bus address of CPU physical 0, as mapped by rp1InboundWindows
	gemDMAOffset = 0x10_00000000
)

// gemRegIO adapts the package MMIO helpers to gem.RegIO.
type gemRegIO struct{}

func (gemRegIO) Read32(addr uint64) uint32       { return Read32(addr) }
func (gemRegIO) Write32(addr uint64, val uint32) { Write32(addr, val) }

// gemDMA adapts a dma.Region and the ARM cache operations to gem.DMA.
type gemDMA struct {
	region *dma.Region
}

func (d *gemDMA) Reserve(size, align int) (uint, []byte) { return d.region.Reserve(size, align) }

// ReserveCoherent backs the descriptor rings, which receive no cache
// maintenance: the 16-byte descriptors share cache lines, so a cacheable ring
// would clobber concurrent hardware updates. The region must therefore be
// mapped non-cacheable, such as DMACoherentBase/DMACoherentSize.
func (d *gemDMA) ReserveCoherent(size, align int) (uint, []byte) {
	return d.region.Reserve(size, align)
}

func (d *gemDMA) Release(addr uint)         { d.region.Release(addr) }
func (d *gemDMA) Bus(cpu uint64) uint64     { return gemDMAOffset + cpu }
func (d *gemDMA) Clean(addr uint, size int) { ARM.CleanDataCacheRange(uintptr(addr), uintptr(size)) }
func (d *gemDMA) Invalidate(addr uint, size int) {
	ARM.InvalidateDataCacheRange(uintptr(addr), uintptr(size))
}
func (d *gemDMA) Barrier() { ARM.DataSynchronizationBarrier() }

// Ethernet is the RP1 Cadence GEM MAC, see [InitGEM].
var Ethernet = &gem.GEM{
	Base: PeripheralAddress(gemBase),
	Reg:  gemRegIO{},
}

// gemMDIO implements phy.MIIM over the GEM clause 22 transport, reporting a
// management interface timeout as an error. A silent PHY reads all ones
// without error.
type gemMDIO struct{}

func (gemMDIO) ReadPHYRegister(pa, ra int) (uint16, error) {
	data, ok := Ethernet.ReadPHY(pa, ra)

	if !ok {
		return 0, fmt.Errorf("bcm2712: MDIO read timeout (phy %d reg %#02x)", pa, ra)
	}

	return data, nil
}

func (gemMDIO) WritePHYRegister(pa, ra int, data uint16) error {
	if !Ethernet.WritePHY(pa, ra, data) {
		return fmt.Errorf("bcm2712: MDIO write timeout (phy %d reg %#02x)", pa, ra)
	}

	return nil
}

// EthernetPHY is the BCM54213PE behind the RP1 GEM, wired as phy-mode
// "rgmii-id" (the PHY supplies both RGMII clock delays). Its reset line is a
// board GPIO, so ResetLine is left to the board.
var EthernetPHY = &bcm54213.PHY{
	Mode: bcm54213.RGMIIID,
}

// ethernetPHYAddr is the BCM54213PE MDIO address (ethernet-phy@1).
const ethernetPHYAddr = 1

// InitGEM initializes the RP1 GEM MAC with the given DMA region and binds
// [EthernetPHY] to its management interface. It must run after [InitRP1], the
// region must lie in system RAM below 64 GiB to fit the inbound window.
//
// The data path is brought up with Ethernet.Start once the link is resolved.
func InitGEM(region *dma.Region) error {
	if region == nil {
		return errors.New("bcm2712: GEM DMA region required")
	}
	if err := RP1ReadyDMA(); err != nil {
		return err
	}

	Ethernet.Base = PeripheralAddress(gemBase)
	Ethernet.DMA = &gemDMA{region: region}

	if err := Ethernet.Init(); err != nil {
		return err
	}

	return EthernetPHY.Init(ethernetPHYAddr, gemMDIO{})
}

// SetupGEMInterrupt enables the RP1 GEM interrupt, a level source armed with
// IACK. It must run after [InitMSI].
func SetupGEMInterrupt() error {
	if err := SetupRP1MSI(gemMSIVector, gemServiceIRQ); err != nil {
		return err
	}
	Ethernet.EnableInterrupts()
	EnableRP1MSI(gemMSIVector, true)
	return nil
}

// gemServiceIRQ clears the GEM interrupt causes before re-arming the vector.
func gemServiceIRQ() {
	Ethernet.HandleIRQ()
	AckRP1MSI(gemMSIVector)
}
