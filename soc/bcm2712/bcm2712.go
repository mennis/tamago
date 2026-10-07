// BCM2712 SoC support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package bcm2712 provides support to Go bare metal unikernels written using
// the TamaGo framework on BCM2712 SoCs (e.g. Raspberry Pi 5).
//
// This package is only meant to be used with `GOOS=tamago GOARCH=arm64` as
// supported by the TamaGo framework for bare metal Go, see
// https://github.com/usbarmory/tamago.
package bcm2712

import (
	// for //go:linkname
	_ "unsafe"

	"github.com/usbarmory/tamago/arm64"
	"github.com/usbarmory/tamago/internal/reg"
)

const refFreq int64 = 1e9

// GPU_BUS_OFFSET is the offset for CPU-to-GPU bus address translation, GPU
// bus address 0xC0000000 maps to ARM physical 0x00000000.
const GPU_BUS_OFFSET uint32 = 0xC0000000

// SysTimerFreq is the frequency (Hz) of the BCM2712 free running system timer.
const SysTimerFreq int64 = 1000000

// The MMIO bases below are statically initialized, rather than left zero until
// Init, as the runtime reaches the RNG and Nanotime during schedinit, before
// Hwinit1 runs.

// peripheralBase is the RP1 southbridge peripheral base.
var peripheralBase uint64 = 0x1f00000000

// socPeripheralBase is the CPU-visible base of the SoC bus, child address
// 0x7c000000 maps to 0x10_7c000000 (device tree soc@107c000000).
var socPeripheralBase uint64 = 0x107c000000

// ARM processor instance.
//
// TimerMultiplier is left zero as the generic timer is not set up on this SoC,
// the system timer drives Nanotime instead. Zero is the safe value: SetTime and
// SetAlarm decline on it, while a multiplier derived from SysTimerFreq would be
// applied to CNTPCT, which runs at CNTFRQ. GetTime has no such guard and
// returns a constant.
var ARM = &arm64.CPU{}

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100000 // 1 MB

//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 {
	// guard against an unset base, Nanotime is sampled during schedinit
	if socPeripheralBase == 0 {
		return ARM.TimerOffset
	}
	return read_systimer()*(refFreq/SysTimerFreq) + ARM.TimerOffset
}

// Init takes care of the lower level BCM2712 initialization triggered
// early in runtime setup (e.g. runtime/goos.Hwinit1).
func Init(rp1Base uint64, socBase uint64) {
	peripheralBase = rp1Base
	socPeripheralBase = socBase

	ARM.Init()
	ARM.EnableCache()

	UART10.Init()
}

// PeripheralAddress returns the absolute address for an RP1 peripheral.
//
// The address only decodes once the RP1 PCIe link is up, as firmware leaves it
// down on bare metal boot: callers must run [InitRP1] (and check [RP1Ready])
// first, board/raspberrypi/pi5 does so during Hwinit1.
func PeripheralAddress(offset uint64) uint64 {
	return peripheralBase + offset
}

// SoCPeripheralAddress returns the absolute address for a SoC-internal peripheral.
func SoCPeripheralAddress(offset uint64) uint64 {
	return socPeripheralBase + offset
}

// Read32 reads a 32-bit value from a 64-bit address.
func Read32(addr uint64) uint32 {
	return reg.Read32At(addr)
}

// Write32 writes a 32-bit value to a 64-bit address.
func Write32(addr uint64, val uint32) {
	reg.Write32At(addr, val)
}

// defined in timer.s
func read_systimer() int64
func Busyloop(count uint32)
