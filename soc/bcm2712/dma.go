// BCM2712 SoC DMA support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.
//
// RP1 DMA is not implemented. The controller is a Synopsys DesignWare AXI DMAC
// (device tree compatible "snps,axi-dma-1.01a", RP1 datasheet §9, Linux
// drivers/dma/dw-axi-dmac/). As a PCIe bus master its addresses must be
// translated through the RP1 dma-ranges inbound window, not given as CPU
// physical addresses.

package bcm2712

import (
	"errors"

	"github.com/usbarmory/tamago/dma"
)

// errNotImplemented is returned by all RP1 DMA operations.
var errNotImplemented = errors.New("bcm2712: RP1 DMA is not implemented (snps,axi-dma-1.01a DesignWare AXI DMAC + PCIe dma-ranges translation required)")

// DMAController represents the RP1 DMA controller, a Synopsys DesignWare AXI
// DMAC which is not yet implemented.
type DMAController struct {
	region *dma.Region
}

// DMA provides access to the RP1 DMA controller.
var DMA = DMAController{}

// Init records the DMA region and returns an error, as RP1 DMA is not
// implemented. It performs no MMIO.
func (c *DMAController) Init(rgn *dma.Region) error {
	c.region = rgn
	return errNotImplemented
}

// Copy performs a RAM to RAM transfer, it always returns an error as RP1 DMA is
// not implemented.
func (c *DMAController) Copy(src uint64, size uint64, dst uint64) error {
	return errNotImplemented
}

// Copy2D performs a 2D RAM to RAM transfer with stride, it always returns an
// error as RP1 DMA is not implemented.
func (c *DMAController) Copy2D(src uint64, dst uint64, xLength uint32, yLength uint32, srcStride uint32, dstStride uint32) error {
	return errNotImplemented
}
