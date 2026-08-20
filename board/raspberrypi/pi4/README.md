TamaGo - bare metal Go - Raspberry Pi 4 support
==============================================

tamago | https://github.com/usbarmory/tamago

Copyright (c) The TamaGo Authors. All Rights Reserved.

Introduction
============

TamaGo is a framework that enables compilation and execution of unencumbered Go
applications on bare metal ARM64 processors.

The [pi4](https://github.com/usbarmory/tamago/tree/master/board/raspberrypi/pi4)
package provides support for the [Raspberry Pi 4 Model B](https://www.raspberrypi.com/products/raspberry-pi-4-model-b/)
single board computer, and for the other BCM2711 boards (Pi 400 and CM4).

Documentation
=============

For TamaGo see its [repository](https://github.com/usbarmory/tamago) and
[project wiki](https://github.com/usbarmory/tamago/wiki) for information.

The package API documentation can be found on
[pkg.go.dev](https://pkg.go.dev/github.com/usbarmory/tamago).

Supported hardware
==================

| SoC              | Board                                                                                       | SoC package                                                            | Board package                                                                  |
|------------------|---------------------------------------------------------------------------------------------|------------------------------------------------------------------------|--------------------------------------------------------------------------------|
| Broadcom BCM2711 | [Raspberry Pi 4 Model B](https://www.raspberrypi.com/products/raspberry-pi-4-model-b/)      | [bcm2711](https://github.com/usbarmory/tamago/tree/master/soc/bcm2711) | [pi4](https://github.com/usbarmory/tamago/tree/master/board/raspberrypi/pi4)   |

The BCM2711 is a quad-core ARM Cortex-A72 processor (ARMv8) used in the
Raspberry Pi 4, Pi 400 and CM4.

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
	_ "github.com/usbarmory/tamago/board/raspberrypi/pi4"
)
```

Build the Go application executable:

```
GOOS=tamago GOARCH=arm64 ${TAMAGO} build -ldflags "-T 0x00081000 -R 0x4000" -o example.elf example.go
```

Note the link address: the firmware loads at `0x80000`, and the kernel is linked
one page **above** it so a reset trampoline placed at the load address is not
overwritten by the image that jumps to it. See `test-pi4/hwcheck/Makefile` for a
complete boot-image flow.

Executing and debugging
=======================

The Raspberry Pi 4 boots from an SD card formatted with a FAT32 partition
carrying the usual Raspberry Pi firmware files.

Configuration file `config.txt`:

```
enable_uart=1
dtoverlay=disable-bt

kernel=example.bin
kernel_address=0x80000
arm_64bit=1

disable_commandline_tags=1
os_check=0
```

Key settings:

- `dtoverlay=disable-bt`: **required**. Without it the firmware wires the PL011
  to the onboard Bluetooth controller and puts the mini-UART on GPIO14/15
  instead. The board package then programs a PL011 nothing is listening to and
  the board boots silently, with no visible fault to explain it. This is the
  single most common cause of a dead console on this board.
- `enable_uart=1`: keeps the VPU clock fixed so the console stays at 115200 baud
- `arm_64bit=1`: ensures the payload is started in AArch64 mode
- `os_check=0`: disables Linux kernel format checks for bare-metal payloads
- `kernel_address=0x80000`: the firmware's default, and what the board package's
  RAM region assumes

What the firmware hands over, measured on a Pi 4 Model B Rev 1.5 (2GB): entry at
**EL2**, MMU and caches **on**, and the flattened device tree pointer in **x0**
(observed `0x2eff1e00`). Unlike a QEMU smoke recipe there is **no parked DTB** —
x0 is the only route to a device tree, so a program that wants one must take the
pointer before the runtime overwrites the register.

Console access
==============

Standard output is available on the GPIO header at 115200 8N1:

| Signal | GPIO   | Header Pin |
|--------|--------|------------|
| TX     | GPIO14 | Pin 8      |
| RX     | GPIO15 | Pin 10     |
| GND    | -      | Pin 6      |

The board package muxes GPIO14/15 to ALT0 itself during `Init` rather than
trusting whatever pin function the firmware or a previous Linux boot's pinctrl
driver left behind. That is measured as necessary on real hardware, not
defensive coding.

`EnableDisplayConsole` additionally mirrors all console output — including panic
and throw messages — to an attached HDMI display. It must be called after the
scheduler is running, and the serial console remains primary.

Peripheral addresses
====================

The BCM2711's peripherals are used through the ARM's view under **Low Peripheral
mode**, which is the mode the Raspberry Pi firmware leaves the SoC in. The
datasheet's legacy `0x7Enn_nnnn` master addresses appear to the ARM at
`0xFEnn_nnnn`, confirmed by this board's own device tree
(`/proc/device-tree/soc/ranges = <0x7e000000 0x0 0xfe000000 0x01800000>`) and by
the PL011 answering at `0xfe201000` on silicon.

Anything handed to the VideoCore must live below `0x40000000`: the GPU alias
covers only the low gigabyte (`dma-ranges = <0xc0000000 0x0 0x0 0x40000000>`). A
buffer above it is not slow, it is unreachable.

Memory
======

The runtime owns a single flat RAM region whose size is fixed at **link time**,
not discovered at boot: the very first instructions of CPU init read it to place
the boot stack (which is also the heap ceiling on this sbrk platform) and to
bound the MMU map, all long before the VideoCore mailbox that could report the
memory layout can answer. It is therefore a link-time constant
(`runtime/goos.RamSize`).

The default is **943.5 MiB**, `[0x80000, 0x3b000000)`, and it is deliberately
conservative:

- The BCM2711 physical memory map is **not contiguous**. On the bench Pi 4 the
  device tree reports bank0 as `[0, 0x3b400000)` — 948MB, matching
  `vcgencmd get_mem arm` — then a hole, then bank1 from `0x40000000`. The hole
  is the VideoCore carve-out, and the firmware owns it: a measured framebuffer
  grant came back at `0x3eb3f000`. The flat region must not span it, because the
  sbrk heap grows toward the top of the region and would eventually hand
  firmware memory to the allocator.
- **The carve-out is not a fixed boundary on this SoC.** Its size is whatever
  `gpu_mem` in `config.txt` says, so its base moves with the contents of the
  boot partition rather than with the hardware. A larger `gpu_mem` pushes the
  base *down*. This is the one way BCM2711 is harder than BCM2712, where
  `0x3f000000` is fixed and a board package can simply encode it. The default
  here stops 4MB below the observed bank0 end, which is right for the stock
  `gpu_mem` with margin for a modestly larger one — but it is a **default, not a
  measurement that holds everywhere**.

`VerifyRAMSize` (`ramsize.go`) is the other half of that bargain. Once the
scheduler is running it asks `GET_VC_MEMORY` where the carve-out actually
starts on *this* board with *this* `config.txt`, and reports whether the linked
region reaches into it (`RAMReport.SpansCarveOut`). Programs that will run on
hardware they did not configure should call it from `main` and treat a set
`SpansCarveOut` as fatal.

To link a different region size, use the `linkramsize` build tag. The board's
default `mem.go` is tagged `//go:build !linkramsize`; supplying the tag replaces
it with your own definition. Add one file to your application:

```golang
//go:build linkramsize

package main

import _ "unsafe"

//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x38000000 - 0x80000 // lowered for gpu_mem=128
```

and build with `-tags linkramsize`.

The carve-out window `[0x3b000000, 0x40000000)` itself is mapped **normal
cacheable, execute-never** through arm64's reserved-window hook (see
`soc/bcm2711/mem.go`), *outside* the runtime's RAM region. Both halves matter:
outside, so the heap can never be placed in it; cacheable, so a framebuffer
granted there can be drawn with ordinary Go stores. Left Device-mapped, every
byte-width or unaligned write into a `framebuffer.Surface` would fault.

Not supported
=============

- **The ACT and PWR LEDs.** On the Pi 4 these moved off the SoC GPIO block onto
  a firmware-managed GPIO expander, so unlike `pi1`/`pi2` they cannot be driven
  with `bcm2711.NewGPIO` — the mailbox is the only path. This package ships no
  `LED` method rather than an untested one, and consequently does not implement
  the `board/raspberrypi` `pi.Board` interface.
- **Ethernet, USB and SDHCI.** No drivers yet.

License
=======

tamago | https://github.com/usbarmory/tamago
Copyright (c) The TamaGo Authors. All Rights Reserved.

This project is distributed under the BSD-style license found in the
[LICENSE](https://github.com/usbarmory/tamago/blob/master/LICENSE) file.
