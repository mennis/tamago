// BCM2711 SoC GPIO support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"fmt"
	"sync"
)

// GPIO register offsets, relative to GPIOOffset (bcm2711.go).
//
// Function select, set/clear and level match the classic Broadcom layout, see
// soc/bcm2835/gpio.go. The pull registers do not, see below.
const (
	GPFSEL0 = 0x00 // GPIO Function Select 0, pins 0-9
	GPFSEL1 = 0x04 // pins 10-19
	GPFSEL2 = 0x08 // pins 20-29
	GPFSEL3 = 0x0c // pins 30-39
	GPFSEL4 = 0x10 // pins 40-49
	GPFSEL5 = 0x14 // pins 50-57

	GPSET0 = 0x1c // GPIO Pin Output Set 0, pins 0-31
	GPSET1 = 0x20 // pins 32-57

	GPCLR0 = 0x28 // GPIO Pin Output Clear 0, pins 0-31
	GPCLR1 = 0x2c // pins 32-57

	GPLEV0 = 0x34 // GPIO Pin Level 0, pins 0-31
	GPLEV1 = 0x38 // pins 32-57

	// GPIO_PUP_PDN_CNTRL_REG0..3 (datasheet §5.3) replace the clocked
	// GPPUD and GPPUDCLK sequence entirely: two bits per pin, 16 pins per
	// register, no wait then latch. See [GPIO.PullMode] for the encoding.
	GPIO_PUP_PDN_CNTRL_REG0 = 0xe4 // pins 0-15
	GPIO_PUP_PDN_CNTRL_REG1 = 0xe8 // pins 16-31
	GPIO_PUP_PDN_CNTRL_REG2 = 0xec // pins 32-47
	GPIO_PUP_PDN_CNTRL_REG3 = 0xf0 // pins 48-57
)

// GPIO function selections (BCM2711 ARM Peripherals datasheet §5.2, table
// unchanged from BCM2835).
const (
	GPIO_INPUT  = 0b000
	GPIO_OUTPUT = 0b001
	GPIO_FN0    = 0b100
	GPIO_FN1    = 0b101
	GPIO_FN2    = 0b110
	GPIO_FN3    = 0b111
	GPIO_FN4    = 0b011
	GPIO_FN5    = 0b010
)

// GPIO_PUP_PDN_CNTRL_REGn field values. The encoding is reversed from the
// BCM2835 GPPUD register, where 1 is pull-down and 2 is pull-up, so a value
// ported across unchanged applies the opposite pull and reads on an input line
// as a wiring fault rather than as a software error.
const (
	GPIO_PULL_NONE = 0b00
	GPIO_PULL_UP   = 0b01
	GPIO_PULL_DOWN = 0b10
)

// gpioMu guards the read-modify-write of GPFSELn and GPIO_PUP_PDN_CNTRL_REGn,
// each of which packs several pins into one register, so that calls on two pins
// sharing a register cannot clobber each other's field.
var gpioMu sync.Mutex

// GPIO instance
type GPIO struct {
	num int
}

type GPIOFunction uint32

// NewGPIO gets access to a single GPIO line. BCM2711 exposes GPIO0-57.
func NewGPIO(num int) (*GPIO, error) {
	if num > 57 || num < 0 {
		return nil, fmt.Errorf("invalid GPIO number %d", num)
	}

	return &GPIO{num: num}, nil
}

// Out configures a GPIO as output.
func (gpio *GPIO) Out() {
	gpio.SelectFunction(GPIO_OUTPUT)
}

// In configures a GPIO as input.
func (gpio *GPIO) In() {
	gpio.SelectFunction(GPIO_INPUT)
}

// SelectFunction selects the function of a GPIO line. It is a read-modify-write
// as GPFSELn packs ten pins into one register at three bits each.
func (gpio *GPIO) SelectFunction(n GPIOFunction) (err error) {
	if n > 0b111 {
		return fmt.Errorf("invalid GPIO function %d", n)
	}

	register := PeripheralAddress(GPIOOffset + GPFSEL0 + 4*uint64(gpio.num/10))
	shift := uint32((gpio.num % 10) * 3)
	mask := uint32(0x7 << shift)

	gpioMu.Lock()
	defer gpioMu.Unlock()

	val := Read32(register)
	val &= ^mask
	val |= (uint32(n) << shift) & mask

	Write32(register, val)

	return
}

// GetFunction gets the current function of a GPIO line.
func (gpio *GPIO) GetFunction() GPIOFunction {
	register := PeripheralAddress(GPIOOffset + GPFSEL0 + 4*uint64(gpio.num/10))
	shift := uint32((gpio.num % 10) * 3)

	return GPIOFunction(Read32(register)>>shift) & 0x7
}

// High configures a GPIO signal as high.
func (gpio *GPIO) High() {
	register := PeripheralAddress(GPIOOffset + GPSET0 + 4*uint64(gpio.num/32))
	shift := uint32(gpio.num % 32)

	Write32(register, 1<<shift)
}

// Low configures a GPIO signal as low.
func (gpio *GPIO) Low() {
	register := PeripheralAddress(GPIOOffset + GPCLR0 + 4*uint64(gpio.num/32))
	shift := uint32(gpio.num % 32)

	Write32(register, 1<<shift)
}

// Value returns the GPIO signal level.
func (gpio *GPIO) Value() (high bool) {
	register := PeripheralAddress(GPIOOffset + GPLEV0 + 4*uint64(gpio.num/32))
	shift := uint32(gpio.num % 32)

	return (Read32(register)>>shift)&0x1 != 0
}

// PullMode sets the line's pull state to one of GPIO_PULL_NONE, GPIO_PULL_UP
// or GPIO_PULL_DOWN.
//
// This is a direct read-modify-write. The clocked GPPUD and GPPUDCLK dance the
// older SoCs require does not apply, there is no clock register to write and
// performing it against these offsets would corrupt an unrelated one. The
// encoding differs as well, see the GPIO_PULL_* constants.
func (gpio *GPIO) PullMode(val uint32) error {
	if val > GPIO_PULL_DOWN {
		return fmt.Errorf("invalid GPIO pull mode %d", val)
	}

	register := PeripheralAddress(GPIOOffset + GPIO_PUP_PDN_CNTRL_REG0 + 4*uint64(gpio.num/16))
	shift := uint32((gpio.num % 16) * 2)
	mask := uint32(0x3 << shift)

	gpioMu.Lock()
	defer gpioMu.Unlock()

	r := Read32(register)
	r &= ^mask
	r |= (val << shift) & mask

	Write32(register, r)

	return nil
}
