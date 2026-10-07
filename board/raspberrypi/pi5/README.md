TamaGo - bare metal Go - Raspberry Pi 5 support
===============================================

tamago | https://github.com/usbarmory/tamago

Copyright (c) The TamaGo Authors. All Rights Reserved.

Introduction
============

TamaGo is a framework that enables compilation and execution of unencumbered Go
applications on bare metal ARM64 processors.

The [pi5](https://github.com/usbarmory/tamago/tree/master/board/raspberrypi/pi5)
package provides support for the [Raspberry Pi 5](https://www.raspberrypi.com/products/raspberry-pi-5/)
single board computer.

Documentation
=============

For TamaGo see its [repository](https://github.com/usbarmory/tamago) and
[project wiki](https://github.com/usbarmory/tamago/wiki) for information.

The package API documentation can be found on
[pkg.go.dev](https://pkg.go.dev/github.com/usbarmory/tamago).

Supported hardware
==================

| SoC              | Board                                                                  | SoC package                                                            | Board package                                                                |
|------------------|------------------------------------------------------------------------|------------------------------------------------------------------------|------------------------------------------------------------------------------|
| Broadcom BCM2712 | [Raspberry Pi 5](https://www.raspberrypi.com/products/raspberry-pi-5/) | [bcm2712](https://github.com/usbarmory/tamago/tree/master/soc/bcm2712) | [pi5](https://github.com/usbarmory/tamago/tree/master/board/raspberrypi/pi5) |

The BCM2712 is a quad-core ARM Cortex-A76 processor (ARMv8.2) used in the
Raspberry Pi 5, most external I/O is provided by the RP1 southbridge over PCIe.

Compiling
=========

Build the [TamaGo compiler](https://github.com/usbarmory/tamago-go)
(or use the [latest binary release](https://github.com/usbarmory/tamago-go/releases/latest)):

```
wget https://github.com/usbarmory/tamago-go/archive/refs/tags/latest.zip
unzip latest.zip
cd tamago-go-latest/src && ./all.bash
cd ../bin && export TAMAGO=`pwd`/go
```

Go applications are simply required to import the relevant board package, to
ensure that hardware initialization and runtime support take place:

```golang
import (
	_ "github.com/usbarmory/tamago/board/raspberrypi/pi5"
)
```

Build the Go application executable:

```
GOOS=tamago GOARCH=arm64 ${TAMAGO} build -ldflags "-T 0x00081000 -R 0x4000" -o example.elf example.go
```

The firmware loads the kernel at `0x80000` and jumps to its first byte, while
the Go entry point lies within `.text`. The executable is therefore linked one
page above the load address and converted to a raw image, with a 16-byte reset
trampoline (`ldr x16, #8; br x16; .quad entry`) prepended at `0x80000`:

```
aarch64-elf-objcopy -O binary -R .note.go.pvh -R .note.go.buildid -R .note.gnu.build-id example.elf kern.bin
ENTRY=$(aarch64-elf-readelf -h example.elf | awk '/Entry point/{print $4}')
{
	printf '\x50\x00\x00\x58\x00\x02\x1f\xd6'
	perl -e 'print pack("Q<", hex(shift))' $ENTRY
	head -c 4080 /dev/zero
	cat kern.bin
} > example.bin
```

Executing and debugging
=======================

The Raspberry Pi 5 boot firmware is stored in EEPROM, the SD card only needs a
FAT32 partition with `config.txt` and the kernel image.

Configuration file `config.txt`:

```
enable_uart=1

kernel=example.bin
kernel_address=0x80000
arm_64bit=1

disable_commandline_tags=1
os_check=0
```

Key settings:

- `arm_64bit=1`: ensures the payload is started in AArch64 mode
- `os_check=0`: disables Linux kernel format checks for bare-metal payloads
- `kernel_address=0x80000`: the load address the reset trampoline is placed at,
  and the base of the runtime RAM region

Console access
==============

Standard output is available on the 3-pin debug UART connector (UART10) at
115200 8N1.

Peripheral addresses
====================

SoC peripherals are mapped at CPU physical `0x10_7c000000` onwards, while the
RP1 peripherals are accessed through the PCIe window at `0x1f_00000000`.

The firmware leaves the RP1 PCIe link down, the board package brings it up
during initialization and panics if it fails, so RP1 peripherals can be used as
soon as `main` runs.

Memory
======

The runtime RAM region size is fixed at link time, as early CPU initialization
uses it to place the boot stack and bound the MMU map before the firmware
mailbox can report the memory layout.

The default is `[0x80000, 0x3f000000)`, on every Pi 5 variant, as the physical
memory map is not contiguous: the firmware owns `[0x3f800000, 0x40000000)` and
may grant buffers as low as `0x3f400000`, with RAM resuming at `0x40000000`.
The heap grows toward the top of the region, which must therefore not span the
firmware carve-out.

To link a different region size, use the `linkramsize` build tag, which
replaces the board default `mem.go` with your own definition:

```golang
//go:build linkramsize

package main

import _ "unsafe"

//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x4_0000_0000 - 0x80000 // 16 GB - ramStart
```

A region larger than the default spans the firmware carve-out. `VerifyRamSize`
reports, once the scheduler is running, the linked size against the board RAM
size and whether the region overlaps the carve-out.

License
=======

tamago | https://github.com/usbarmory/tamago
Copyright (c) The TamaGo Authors. All Rights Reserved.

This project is distributed under the BSD-style license found in the
[LICENSE](https://github.com/usbarmory/tamago/blob/master/LICENSE) file.
