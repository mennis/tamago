// Raspberry Pi support for tamago/arm64
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build !linkprintk && arm64

package pi

import (
	"sync/atomic"
)

// This file holds the display mirror plumbing shared by the 64-bit Raspberry Pi
// boards and deliberately does not define printk.
//
// It used to. That definition wrote to one SoC's UART unconditionally, which
// made a package named for the whole family specific to one member of it, and
// since runtime/goos.Printk admits exactly one definition a board could not
// import this package and override it either. printk therefore belongs to the
// board packages, which know which UART they have, and what remains here is an
// atomic hook a board's printk consults to mirror bytes to a display.

// displayConsole optionally mirrors every printk byte to an attached display.
// It is an atomic pointer because printk runs from any context, including
// throws and exception handlers, so it must load without locks.
var displayConsole atomic.Pointer[func(byte)]

// SetDisplayConsole installs fn as a mirror for all console output, or removes
// it when nil.
//
// fn runs on every printk byte from arbitrary context, concurrently from
// multiple cores and reentrantly from a panic, so it must be non-blocking,
// allocation free and lock free, and must guard its own mutable state. The
// serial console remains primary and has already carried every byte, so a
// mirror may drop bytes but must not panic.
func SetDisplayConsole(fn func(byte)) {
	if fn == nil {
		displayConsole.Store(nil)
		return
	}
	displayConsole.Store(&fn)
}

// DisplayConsole returns the installed mirror, or nil if there is none. It
// exists for the board printk implementations, which live outside this package
// and cannot reach displayConsole directly.
func DisplayConsole() func(byte) {
	if fn := displayConsole.Load(); fn != nil {
		return *fn
	}

	return nil
}
