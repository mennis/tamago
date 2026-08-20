// Raspberry Pi 4 console support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkprintk

package pi4

import (
	_ "unsafe"

	pi "github.com/usbarmory/tamago/board/raspberrypi"
	"github.com/usbarmory/tamago/soc/bcm2711"
)

// SetDisplayConsole installs fn as a mirror for all console output, or removes
// it when nil. See pi.SetDisplayConsole for the contract fn must satisfy: it
// runs on every printk byte from arbitrary context, so it must be non-blocking,
// allocation free and lock free, and must never panic. The serial console
// remains primary and has already carried every byte, so a mirror may drop
// bytes.
func SetDisplayConsole(fn func(byte)) {
	pi.SetDisplayConsole(fn)
}

// printk mirrors the runtime's console output onto UART0.
//
//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	// bcm2711.UART0, not a package-level alias: printk runs from arbitrary
	// contexts including the exception handler and everything schedinit does,
	// all of which happen before package-level initialization would populate
	// one. An alias here was nil at Hwinit1 and cost a boot; see Init in pi4.go.
	bcm2711.UART0.Tx(c)

	if fn := pi.DisplayConsole(); fn != nil {
		fn(c)
	}
}
