// Raspberry Pi 5 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkramsize

package pi5

import (
	_ "unsafe"
)

// The Raspberry Pi 5 physical memory map is not contiguous: bank0 ends at
// 0x3f800000, the firmware owns [0x3f800000, 0x40000000) and may grant
// framebuffers as low as 0x3f400000, then RAM resumes at 0x40000000.
//
// The runtime's single flat region must not span that hole, as the heap grows
// toward the boot stack at the region's top and would eventually hand
// firmware-owned memory to the allocator. The default therefore ends at
// bcm2712.FirmwareRAMStart, which is safe on every Pi 5 variant. Larger
// regions can be linked with the linkramsize build tag, see [VerifyRamSize].
//
// The size must be a link-time constant as it is consumed by early CPU
// initialization, to place the boot stack and bound the MMU map, long before
// the firmware mailbox can report the memory layout.
//
//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x3f000000 - 0x00080000 // bank0 up to FirmwareRAMStart
