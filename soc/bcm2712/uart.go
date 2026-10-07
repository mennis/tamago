// BCM2712 SoC PL011 UART support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

// PL011 UART register offsets, uart10 is the debug UART on the Raspberry Pi 5
// UART connector at SoC child address 0x7d001000.
const (
	UART10_BASE = 0x1001000

	// Standard ARM PL011 register offsets from the UART base.
	UART_DR   = 0x00 // Data Register
	UART_FR   = 0x18 // Flag Register
	UART_IBRD = 0x24 // Integer Baud Rate Register
	UART_FBRD = 0x28 // Fractional Baud Rate Register
	UART_LCRH = 0x2c // Line Control Register
	UART_CR   = 0x30 // Control Register
)

// PL011 Flag Register bits.
const (
	FR_BUSY = 1 << 3 // UART busy transmitting
	FR_TXFF = 1 << 5 // Transmit FIFO full
)

// txFIFOSpin bounds the transmit FIFO busy-wait, Tx backs Printk which can run
// before the scheduler or during a panic, so it cannot yield and drops the
// character instead.
const txFIFOSpin = 1 << 20

type pl011 struct {
	// base is the absolute (CPU-visible) PL011 register base address.
	base uint64
}

// uart10Base is a constant rather than SoCPeripheralAddress(UART10_BASE), as
// package initialization runs after schedinit, which can already print.
const uart10Base = 0x107c000000 + UART10_BASE

// UART10 is the BCM2712 SoC PL011 debug UART, intended to be used as the
// Raspberry Pi 5 early console.
//
// Its base is valid before Hwinit1 as the runtime can print earlier, the
// firmware (enable_uart=1) has already configured the controller.
var UART10 = &pl011{base: uart10Base}

// Init initializes the PL011 UART for use as a console.
//
// The controller clock, baud rate and pins are left as configured by the
// firmware (stdout-path = "serial10:115200n8"), only the base is recorded.
func (hw *pl011) Init() {
	hw.base = SoCPeripheralAddress(UART10_BASE)
}

// Tx transmits a single character to the serial port.
func (hw *pl011) Tx(c byte) {
	// wait for room in the transmit FIFO, bounded (see txFIFOSpin)
	for i := 0; Read32(hw.base+UART_FR)&FR_TXFF != 0; i++ {
		if i >= txFIFOSpin {
			return
		}
	}

	Write32(hw.base+UART_DR, uint32(c))
}

// Write data from buffer to serial port.
func (hw *pl011) Write(buf []byte) {
	for i := 0; i < len(buf); i++ {
		hw.Tx(buf[i])
	}
}
