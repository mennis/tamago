// Raspberry Pi 5 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package pi5 provides hardware initialization, automatically on import,
// for the Raspberry Pi 5 single board computer.
//
// This package is only meant to be used with `GOOS=tamago GOARCH=arm64` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
package pi5

import (
	_ "unsafe"

	_ "github.com/usbarmory/tamago/board/raspberrypi"
	"github.com/usbarmory/tamago/soc/bcm2712"
)

const (
	rp1Base = 0x1f00000000

	// socBase is the CPU physical base of the SoC bus, the device tree
	// soc@107c000000 maps child address 0x7c000000 to 0x10_7c000000.
	socBase = 0x107c000000
)

// Init takes care of the lower level initialization triggered early in runtime
// setup (post World start).
//
//go:linkname Init runtime/goos.Hwinit1
func Init() {
	bcm2712.Init(rp1Base, socBase)

	// The firmware leaves the RP1 PCIe link down, so nothing at rp1Base
	// decodes until it is brought up. RP1 peripheral APIs cannot report a
	// link failure, therefore a bring-up error is fatal.
	if err := bcm2712.InitRP1(); err != nil {
		panic("pi5: RP1 southbridge bring-up failed: " + err.Error())
	}
}
