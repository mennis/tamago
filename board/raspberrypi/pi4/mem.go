// Raspberry Pi 4 support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkramsize

package pi4

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/soc/bcm2711"
)

// The Raspberry Pi 4 ships with 1, 2, 4 or 8GB and its physical memory map is
// not contiguous: the low gigabyte ends at a VideoCore carve-out, then RAM
// resumes at 0x40000000. The firmware owns that hole and grants framebuffers
// inside it.
//
// The runtime's single flat region must not span it. The heap grows toward the
// boot stack at the region's top, so a region crossing the carve-out eventually
// hands firmware-owned memory to the allocator, the live framebuffer first.
//
// The carve-out's size is whatever gpu_mem in config.txt says, so its base
// moves with the boot partition rather than with the hardware and no single
// address is correct for every Pi 4. The default below is therefore
// conservative rather than measured: it ends at bcm2711.RAMSafeEnd, right for
// the stock gpu_mem with margin for a modestly larger one. A board configured
// with a much larger gpu_mem must lower it through the linkramsize build tag.
//
//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = bcm2711.RAMSafeEnd - ramStart

// ramStart restates soc/bcm2711/mem.go's runtime/goos.RamStart value, which is
// not exported there. It is the firmware's default kernel_address: the image is
// loaded at 0x80000 and the runtime's region begins where the image begins.
const ramStart = 0x00080000
