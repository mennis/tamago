// BCM2712 RP1 southbridge support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"errors"
	"time"

	"github.com/usbarmory/tamago/soc/bcm2712/pcie"
)

// rp1RCBase is the PCIe2 root complex hosting RP1 (DT pcie@1000120000). RP1
// BAR1 is mapped so that peripheralBase reaches BAR1 offset 0.
const rp1RCBase = 0x10_00120000

// rp1Bus adapts the package MMIO helpers to pcie.RegIO.
type rp1Bus struct{}

func (rp1Bus) Read32(addr uint64) uint32       { return Read32(addr) }
func (rp1Bus) Write32(addr uint64, val uint32) { Write32(addr, val) }

// rp1Delay busy-waits on the system timer, as RP1 is brought up during
// Hwinit1 before the scheduler can serve time.Sleep.
func rp1Delay(d time.Duration) {
	deadline := nanotime() + int64(d)
	for nanotime() < deadline {
	}
}

var rp1Controller *pcie.Controller

// rp1InboundWindows are the PCIe->CPU windows from the DT dma-ranges: RP1
// peripheral space (peer access), system RAM (DMA) and the MIP0 MSI-X doorbell
// (see msi.go).
var rp1InboundWindows = []pcie.InboundWindow{
	{Size: 0x00400000, PCIOffset: 0x00_00000000, CPUAddr: 0x1f_00000000},
	{Size: 0x10_00000000, PCIOffset: 0x10_00000000, CPUAddr: 0x00000000},
	{Size: 0x00001000, PCIOffset: 0xff_fffff000, CPUAddr: 0x10_00130000},
}

// InitRP1 brings up the PCIe2 root complex, enumerates RP1, validates the BAR1
// MMIO path and programs the inbound DMA windows. The root port memory space
// enable is set only after link up.
//
// It must run after [Init] and before any RP1 driver is used. A repeat call
// re-validates the link without retraining it.
func InitRP1() error {
	if rp1Controller == nil {
		rp1Controller = pcie.New(rp1Bus{}, rp1Delay, ARM.DataSynchronizationBarrier, rp1RCBase, peripheralBase)
		rp1Controller.EnableDMA(rp1InboundWindows)
	}
	return rp1Controller.Init()
}

// RP1Ready validates the RP1 BAR1 MMIO path (link active, decode enabled,
// chip ID readable) without disturbing it.
func RP1Ready() error {
	if rp1Controller == nil {
		return errors.New("bcm2712: RP1 not initialized")
	}
	return rp1Controller.Ready()
}

// RP1ReadyDMA validates the RP1 inbound DMA path (windows programmed, bus
// master enabled) in addition to [RP1Ready].
func RP1ReadyDMA() error {
	if rp1Controller == nil {
		return errors.New("bcm2712: RP1 not initialized")
	}
	return rp1Controller.ReadyDMA()
}
