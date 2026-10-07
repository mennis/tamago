// BCM2712 SoC support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// func read_systimer() int64
TEXT ·read_systimer(SB),$0-8
	MOVD	·socPeripheralBase(SB), R2
	ADD	$0x00003000, R2
read_retry:
	// MOVWU as MOVW is a sign-extending load (LDRSW)
	MOVWU	8(R2), R1	// upper 32-bits
	MOVWU	4(R2), R0	// lower 32-bits
	MOVWU	8(R2), R3	// re-read upper
	CMP	R1, R3
	BNE	read_retry
	ORR	R1<<32, R0, R0
	MOVD	R0, ret+0(FP)
	RET

// func Busyloop(count uint32)
//
// Zero is checked before the first decrement, which would otherwise underflow
// into 2^32 iterations.
TEXT ·Busyloop(SB),$0-4
	MOVWU	count+0(FP), R0
	CBZ	R0, busyloop_done
busyloop:
	SUBW	$1, R0, R0
	CBNZ	R0, busyloop
busyloop_done:
	RET
