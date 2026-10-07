// BCM2712 SoC GPIO support (RP1 southbridge)
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"fmt"
)

// RP1 GPIO register block offsets
const (
	IO_BANK0_BASE   = 0x0d0000
	SYS_RIO0_BASE   = 0x0e0000
	PADS_BANK0_BASE = 0x0f0000

	// RP1 atomic register aliases (datasheet §2.4), writing a mask sets or
	// clears only those bits in every RP1 register block.
	ATOMIC_SET = 0x2000
	ATOMIC_CLR = 0x3000

	// RIO register offsets within each atomic region
	RIO_OUT     = 0x00
	RIO_OE      = 0x04
	RIO_SYNC_IN = 0x08

	// pads_bank0 pad control (RP1 datasheet §3.1), the pad gates the pin
	// buffers independently of io_bank0 and sys_rio0 and resets with both
	// output and input disabled.
	PAD_GPIO0 = 0x04   // pad control register of GPIO0; GPIOn at PAD_GPIO0 + num*4
	PAD_OD    = 1 << 7 // output disable (reset 1: driver off, priority over OE)
	PAD_IE    = 1 << 6 // input enable (reset 0: input buffer off)
)

// GPIO function selections (RP1 FUNCSEL values) common to every pin, the
// alternate functions below 5 differ per pin.
const (
	GPIO_FUNC_SYS_RIO GPIOFunction = 5
	GPIO_FUNC_PROC    GPIOFunction = 6
	GPIO_FUNC_PIO     GPIOFunction = 7
	GPIO_FUNC_NULL    GPIOFunction = 31
)

// GPIOFunction represents an RP1 GPIO function select value
type GPIOFunction uint32

// GPIO instance
type GPIO struct {
	num int
}

// NewGPIO gets access to a single GPIO line
func NewGPIO(num int) (*GPIO, error) {
	if num > 27 || num < 0 {
		return nil, fmt.Errorf("invalid GPIO number %d", num)
	}

	return &GPIO{num: num}, nil
}

// padReg returns the pin pad control register offset within pads_bank0.
func (gpio *GPIO) padReg() uint64 {
	return PAD_GPIO0 + uint64(gpio.num)*4
}

// Out configures a GPIO as output using SYS_RIO, enabling the pad output
// driver and input buffer so that Value() reads the driven level back. Other
// pad settings keep their reset values.
func (gpio *GPIO) Out() {
	gpio.SelectFunction(GPIO_FUNC_SYS_RIO)
	Write32(PeripheralAddress(PADS_BANK0_BASE+ATOMIC_CLR+gpio.padReg()), PAD_OD)
	Write32(PeripheralAddress(PADS_BANK0_BASE+ATOMIC_SET+gpio.padReg()), PAD_IE)
	Write32(PeripheralAddress(SYS_RIO0_BASE+ATOMIC_SET+RIO_OE), 1<<uint32(gpio.num))
}

// In configures a GPIO as input using SYS_RIO, enabling the pad input buffer
// and disabling its output driver.
func (gpio *GPIO) In() {
	gpio.SelectFunction(GPIO_FUNC_SYS_RIO)
	Write32(PeripheralAddress(PADS_BANK0_BASE+ATOMIC_SET+gpio.padReg()), PAD_IE|PAD_OD)
	Write32(PeripheralAddress(SYS_RIO0_BASE+ATOMIC_CLR+RIO_OE), 1<<uint32(gpio.num))
}

// High configures a GPIO signal as high.
func (gpio *GPIO) High() {
	Write32(PeripheralAddress(SYS_RIO0_BASE+ATOMIC_SET+RIO_OUT), 1<<uint32(gpio.num))
}

// Low configures a GPIO signal as low.
func (gpio *GPIO) Low() {
	Write32(PeripheralAddress(SYS_RIO0_BASE+ATOMIC_CLR+RIO_OUT), 1<<uint32(gpio.num))
}

// Value returns the GPIO signal level.
func (gpio *GPIO) Value() bool {
	val := Read32(PeripheralAddress(SYS_RIO0_BASE + RIO_SYNC_IN))
	return (val>>uint32(gpio.num))&1 != 0
}

// SelectFunction selects the function of a GPIO line via io_bank0.
func (gpio *GPIO) SelectFunction(n GPIOFunction) error {
	if n > 31 {
		return fmt.Errorf("invalid GPIO function %d", n)
	}

	ctrlAddr := PeripheralAddress(IO_BANK0_BASE + uint64(gpio.num)*8 + 4)
	val := Read32(ctrlAddr)
	val &= ^uint32(0x1F)
	val |= uint32(n) & 0x1F
	Write32(ctrlAddr, val)

	return nil
}
