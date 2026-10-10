// QEMU virt (arm64) support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkprintk

package virt

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/internal/reg"
)

// PL011 UART registers
const (
	UART0_BASE = 0x09000000

	UART_DR = UART0_BASE + 0x00
	UART_FR = UART0_BASE + 0x18
	FR_TXFF = 5
)

// bounded so that a wedged FIFO drops a byte instead of hanging printk
const txFIFOSpin = 1 << 20

//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	for i := 0; i < txFIFOSpin; i++ {
		if !reg.Get(UART_FR, FR_TXFF) {
			break
		}
	}

	reg.Write(UART_DR, uint32(c))
}
