// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

#include "arm64.h"

// func read_midr_el1() uint64
TEXT ·read_midr_el1(SB),$0-8
	MRS	MIDR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET
