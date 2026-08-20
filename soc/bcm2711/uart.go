// BCM2711 SoC PL011 UART support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

// UART0 register offsets, relative to UART0Offset (bcm2711.go). Standard
// ARM PL011, unchanged across the BCM283x/BCM2711 family.
const (
	UART_DR   = 0x00 // Data Register
	UART_FR   = 0x18 // Flag Register
	UART_IBRD = 0x24 // Integer Baud Rate Register
	UART_FBRD = 0x28 // Fractional Baud Rate Register
	UART_LCRH = 0x2c // Line Control Register
	UART_CR   = 0x30 // Control Register
	UART_IMSC = 0x38 // Interrupt Mask Set/Clear Register
)

// PL011 Flag Register bits.
const (
	FR_TXFE = 1 << 7 // Transmit FIFO empty
	FR_RXFF = 1 << 6 // Receive FIFO full
	FR_TXFF = 1 << 5 // Transmit FIFO full
	FR_RXFE = 1 << 4 // Receive FIFO empty
	FR_BUSY = 1 << 3 // UART busy transmitting
)

// PL011 Line Control Register bits.
const (
	LCRH_FEN   = 1 << 4 // enable FIFOs
	LCRH_WLEN8 = 0b11 << 5
)

// PL011 Control Register bits.
const (
	CR_UARTEN = 1 << 0
	CR_TXE    = 1 << 8
	CR_RXE    = 1 << 9
)

// spinLimit bounds every busy-wait in this driver, so that a stuck line gives
// up rather than hanging the caller.
const spinLimit = 1 << 20

// UART represents a BCM2711 PL011 UART instance.
//
// It caches no base address, recomputing one through [PeripheralAddress] on
// every access instead. That is deliberate: runtime/goos.Printk is wired to
// this driver, and everything schedinit does runs before a board's Hwinit1 has
// touched a UART, so a throw in that window calls Tx on an uninitialized
// receiver. A cached base would send that write to address 0, while recomputing
// at least reaches the PL011 wherever the firmware left it enabled.
type UART struct {
	// Clock is the UART reference clock in Hz, which the caller must supply:
	// it is set by CPRMAN and the firmware and varies by board and boot
	// configuration, measured 48MHz on a Raspberry Pi 4 Model B.
	Clock uint32
}

// UART0 is the BCM2711 SoC PL011 UART at UART0Offset. It only reaches
// GPIO14/15 when config.txt carries dtoverlay=disable-bt, without which the
// PL011 is wired to Bluetooth and those pins carry the mini UART instead.
var UART0 = &UART{}

// ConfigurePins muxes GPIO14 (TXD0) and GPIO15 (RXD0) to ALT0, the UART0
// function. It is required on real hardware: whatever pin function the firmware
// or a prior pinctrl driver left behind cannot be trusted, and without ALT0 the
// PL011 has no path to the header pins however it is otherwise configured.
func ConfigurePins() (err error) {
	for _, num := range []int{14, 15} {
		gpio, err := NewGPIO(num)
		if err != nil {
			return err
		}

		if err = gpio.SelectFunction(GPIO_FN0); err != nil {
			return err
		}
	}

	return
}

// Init initializes the PL011 UART for the given baud rate and reference clock,
// see [UART.Clock]. Callers must mux the pins first, see [ConfigurePins].
func (hw *UART) Init(baud int, clock uint32) {
	hw.Clock = clock

	cr := PeripheralAddress(UART0Offset + UART_CR)
	fr := PeripheralAddress(UART0Offset + UART_FR)

	// disable before reprogramming, a live baud rate change is undefined
	Write32(cr, 0)

	// wait for any in-flight transmission, bounded
	for i := 0; Read32(fr)&FR_BUSY != 0; i++ {
		if i >= spinLimit {
			break
		}
	}

	// Baud rate divisor, BRD = Clock / (16 * baud), with IBRD its integer
	// part and FBRD its fractional part in 6 bits (PL011 TRM §3.3.6-7).
	// Splitting Clock*4/baud by 64 avoids floating point, as 64 = 16 * 4.
	div := uint32(uint64(hw.Clock) * 4 / uint64(baud))
	ibrd := div / 64
	fbrd := div % 64

	Write32(PeripheralAddress(UART0Offset+UART_IBRD), ibrd)
	Write32(PeripheralAddress(UART0Offset+UART_FBRD), fbrd)

	// 8N1 with FIFOs. IBRD and FBRD must be written first, the divisor is
	// latched by the write to LCRH (TRM §3.3.7).
	Write32(PeripheralAddress(UART0Offset+UART_LCRH), LCRH_FEN|LCRH_WLEN8)

	// this driver is polled
	Write32(PeripheralAddress(UART0Offset+UART_IMSC), 0)

	Write32(cr, CR_UARTEN|CR_TXE|CR_RXE)
}

// Tx transmits a single character to the serial port.
func (hw *UART) Tx(c byte) {
	fr := PeripheralAddress(UART0Offset + UART_FR)
	dr := PeripheralAddress(UART0Offset + UART_DR)

	// wait for room in the transmit FIFO, bounded
	for i := 0; Read32(fr)&FR_TXFF != 0; i++ {
		if i >= spinLimit {
			return
		}
	}

	Write32(dr, uint32(c))
}

// Rx receives a single character from the serial port, non-blocking. The
// second return value indicates whether a character was available.
func (hw *UART) Rx() (c byte, valid bool) {
	fr := PeripheralAddress(UART0Offset + UART_FR)

	if Read32(fr)&FR_RXFE != 0 {
		return 0, false
	}

	dr := PeripheralAddress(UART0Offset + UART_DR)

	return byte(Read32(dr)), true
}

// Write sends buf to the serial port.
func (hw *UART) Write(buf []byte) {
	for i := 0; i < len(buf); i++ {
		hw.Tx(buf[i])
	}
}
