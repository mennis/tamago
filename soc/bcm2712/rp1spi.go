// BCM2712 RP1 SPI support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"fmt"

	"github.com/usbarmory/tamago/soc/bcm2712/spi"
)

// RP1 SPI0 (DT spi@50000, RP1 address 0x40050000), the controller wired to the
// 40-pin header.
const (
	spi0Base = 0x50000

	// ssi_clk, from clk_sys (200 MHz), which needs no programming here
	spi0Clock = 200_000_000

	// RP1 GPIO function selection is per pin, gpio.go only defines the values
	// common to every pin. SPI0 is funcsel 0 on GPIO 8-11.
	spi0MISO = 9
	spi0MOSI = 10
	spi0SCLK = 11
	spi0CE0  = 8 // not muxed, see InitSPI0

	spi0FuncSel = 0
)

// spiRegIO adapts the package MMIO helpers to spi.RegIO.
type spiRegIO struct{}

func (spiRegIO) Read32(addr uint64) uint32       { return Read32(addr) }
func (spiRegIO) Write32(addr uint64, val uint32) { Write32(addr, val) }

// SPI0 is the RP1 SPI0 controller on the 40-pin header, see [InitSPI0].
//
// Chip select must be driven as a GPIO output ([NewGPIO]): the native one is
// asserted per transfer, while devices that delimit commands by chip select
// need it held across a whole message.
//
// The register layout is set explicitly as, despite its DWC_ssi version ID,
// the controller uses the DW_apb_ssi layout (DT compatible "snps,dw-apb-ssi").
var SPI0 = &spi.SPI{
	Base:   PeripheralAddress(spi0Base),
	Reg:    spiRegIO{},
	Clock:  spi0Clock,
	Layout: spi.APB,
}

// InitSPI0 muxes the SPI0 pins and initializes the controller for a serial
// clock of at most speed Hz (SPI0.SCLK reports the achieved rate). Any other
// SPI0 field must be set before calling. It must run after [InitRP1].
//
// Only MOSI and SCLK are muxed. MISO can be muxed by the caller when needed.
// CE0 is left unmuxed so that the non-zero SER value the controller requires
// to clock drives no pin.
func InitSPI0(speed int) error {
	if err := RP1Ready(); err != nil {
		return err
	}

	for _, pin := range []int{spi0MOSI, spi0SCLK} {
		gpio, err := NewGPIO(pin)

		if err != nil {
			return fmt.Errorf("bcm2712: SPI0 pin %d: %v", pin, err)
		}

		// the pad output and input enables reset off, independently of
		// io_bank0
		Write32(PeripheralAddress(PADS_BANK0_BASE+ATOMIC_CLR+gpio.padReg()), PAD_OD)
		Write32(PeripheralAddress(PADS_BANK0_BASE+ATOMIC_SET+gpio.padReg()), PAD_IE)

		if err := gpio.SelectFunction(spi0FuncSel); err != nil {
			return fmt.Errorf("bcm2712: SPI0 pin %d: %v", pin, err)
		}
	}

	SPI0.Base = PeripheralAddress(spi0Base)
	SPI0.Speed = speed

	return SPI0.Init()
}
