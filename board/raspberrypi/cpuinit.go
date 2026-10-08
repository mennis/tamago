// Raspberry Pi early CPU initialization
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build linkcpuinit

package pi

// earlyPeripheralBase is the start of the Device region in the early MMU map
// built by cpuinit.s, set by the board package with a static initializer as it
// is read before any Go code runs.
var earlyPeripheralBase uint32
