// ARM processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// func read_actlr() uint32
TEXT ·read_actlr(SB),$0-4
	// Cortex™-A7 MPCore® Technical Reference Manual r0p5
	//
	// 4.3.31 Auxiliary Control Register

	// Invalidate Entire Instruction Cache
	MOVW	$0, R0
	MCR	15, 0, R0, C7, C5, 0

	MRC	15, 0, R0, C1, C0, 1
	MOVW	R0, ret+0(FP)

	RET

// func write_actlr(aux uint32)
TEXT ·write_actlr(SB),$0-4
	// Cortex™-A7 MPCore® Technical Reference Manual r0p5
	//
	// 4.3.31 Auxiliary Control Register

	// Invalidate Instruction Cache
	MOVW	$0, R1
	MCR	15, 0, R1, C7, C5, 0

	MOVW	aux+0(FP), R0
	MCR	15, 0, R0, C1, C0, 1

	RET

// func cache_disable()
TEXT ·cache_disable(SB),$0
	MRC	15, 0, R1, C1, C0, 0
	BIC	$1<<12, R1			// Disable I-cache
	BIC	$1<<2, R1			// Disable D-cache
	MCR	15, 0, R1, C1, C0, 0
	RET

// func cache_enable()
TEXT ·cache_enable(SB),$0
	MRC	15, 0, R1, C1, C0, 0
	ORR	$1<<12, R1			// Enable I-cache
	ORR	$1<<2, R1			// Enable D-cache
	MCR	15, 0, R1, C1, C0, 0
	RET

// func cache_flush_instruction()
TEXT ·cache_flush_instruction(SB),$0
	MOVW	$0, R0
	MCR	15, 0, R0, C7, C5, 0
	RET
