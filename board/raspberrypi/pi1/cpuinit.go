// Raspberry Pi early CPU initialization
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build linkcpuinit

package pi1

import (
	_ "unsafe"
)

// The BCM2835 peripheral window, for the early MMU map in
// board/raspberrypi/cpuinit.s. Static, because it is read before any Go runs.
//
//go:linkname earlyPeripheralBase github.com/usbarmory/tamago/board/raspberrypi.earlyPeripheralBase
var earlyPeripheralBase uint32 = 0x20000000
