// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package reg provides primitives for retrieving and modifying hardware
// registers.
//
// This package is only meant to be used with `GOOS=tamago` as supported by the
// TamaGo framework for bare metal Go, see https://github.com/usbarmory/tamago.
package reg

import (
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"
)

func Get(addr uint32, pos int) bool {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))
	r := atomic.LoadUint32(reg)

	return (r>>pos)&1 == 1
}

func Set(addr uint32, pos int) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	r |= (1 << pos)

	atomic.StoreUint32(reg, r)
}

func SetTo(addr uint32, pos int, val bool) {
	if val {
		Set(addr, pos)
	} else {
		Clear(addr, pos)
	}
}

func Clear(addr uint32, pos int) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	r &= ^(1 << pos)

	atomic.StoreUint32(reg, r)
}

func GetN(addr uint32, pos int, mask int) uint32 {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))
	r := atomic.LoadUint32(reg)

	return (r >> pos) & uint32(mask)
}

func SetN(addr uint32, pos int, mask int, val uint32) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	r = (r & (^(uint32(mask) << pos))) | ((val & uint32(mask)) << pos)

	atomic.StoreUint32(reg, r)
}

func ClearN(addr uint32, pos int, mask int) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	r &= ^(uint32(mask) << pos)

	atomic.StoreUint32(reg, r)
}

// defined in reg_*.s
func Move(dst uint32, src uint32)

func Read(addr uint32) uint32 {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))
	return atomic.LoadUint32(reg)
}

// defined in reg_*.s
func Write(addr uint32, val uint32)

func Write32(addr uint32, val uint32) {
	Write(addr, val)
}

func WriteBack(addr uint32) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	atomic.StoreUint32(reg, r)
}

func Or(addr uint32, val uint32) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))

	r := atomic.LoadUint32(reg)
	r |= val

	atomic.StoreUint32(reg, r)
}

func Wait(addr uint32, pos int, mask int, val uint32) {
	for GetN(addr, pos, mask) != val {
		runtime.Gosched()
	}
}

func WaitFor(timeout time.Duration, addr uint32, pos int, mask int, val uint32) bool {
	start := time.Now()

	for GetN(addr, pos, mask) != val {
		runtime.Gosched()

		if time.Since(start) >= timeout {
			return false
		}
	}

	return true
}

func WaitSignal(exit chan struct{}, addr uint32, pos int, mask int, val uint32) bool {
	for GetN(addr, pos, mask) != val {
		runtime.Gosched()

		select {
		case <-exit:
			return false
		default:
		}
	}

	return true
}

// Read32At and Write32At access a 32-bit register at a 64-bit address, which
// neither Read/Write (32-bit address) nor Read64/Write64 (64-bit value) cover.
// Such registers exist, the BCM2712 places its GIC-400 above 4 GiB.
//
// The pair is not build tagged so that a single driver can serve both 32-bit
// and 64-bit targets, on the former an address above 4 GiB truncates.
func Read32At(addr uint64) uint32 {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))
	return atomic.LoadUint32(reg)
}

func Write32At(addr uint64, val uint32) {
	reg := (*uint32)(unsafe.Pointer(uintptr(addr)))
	atomic.StoreUint32(reg, val)
}
