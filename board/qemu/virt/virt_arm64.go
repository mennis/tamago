// QEMU virt (arm64) support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package virt provides hardware initialization, automatically on import, for
// the QEMU arm64 `virt` machine, and for hypervisors sharing its guest
// physical layout.
//
// This package is only meant to be used with `GOOS=tamago GOARCH=arm64` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
package virt

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/arm64"
	"github.com/usbarmory/tamago/dma"
)

const (
	dmaSize  = 0x1000000 // 16MB
	dmaStart = 0x40000000 + 0x20000000 - dmaSize
)

//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x40000000

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100000 // 1 MB

// ARM processor instance
var ARM = &arm64.CPU{
	// non-zero until InitGenericTimers, for Nanotime calls before Hwinit1
	TimerMultiplier: 1,
	// a guest drives the virtual timer, the one a hypervisor models
	Timer: arm64.VirtualTimer,
}

//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 {
	return ARM.GetTime()
}

// Init takes care of the lower level initialization triggered early in runtime
// setup (post World start).
//
//go:linkname Init runtime/goos.Hwinit1
func Init() {
	ARM.Init()
	ARM.EnableCache()

	// the counter is already running, only CNTFRQ_EL0 needs reading
	ARM.InitGenericTimers(0, 0)
}

func init() {
	// the DMA region lies above the runtime, where memory is mapped as device
	ARM.ConfigureMMU(dmaStart, dmaStart+dmaSize, 0, arm64.MemoryAttributes|arm64.TTE_BLOCK)

	// allocate global DMA region
	dma.Init(dmaStart, dmaSize)
}
