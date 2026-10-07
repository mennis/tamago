// Raspberry Pi 5 console support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkprintk

package pi5

import (
	_ "unsafe"

	pi "github.com/usbarmory/tamago/board/raspberrypi"
	"github.com/usbarmory/tamago/soc/bcm2712"
)

// printk mirrors the runtime's console output onto UART10.
//
//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	bcm2712.UART10.Tx(c)

	if fn := pi.DisplayConsole(); fn != nil {
		fn(c)
	}
}
