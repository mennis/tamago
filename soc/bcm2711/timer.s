// BCM2711 SoC timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// func read_systimer() int64
TEXT ·read_systimer(SB),$0-8
	MOVD	·peripheralBase(SB), R2
	ADD	$0x00003000, R2	// SystemTimerOffset
read_retry:
	// MOVWU, not MOVW: MOVW is a signed 32-bit load on arm64, so once CLO
	// passes 0x80000000 the ORR below cannot clear the sign extension and the
	// counter reads negative. The timer runs from power-on rather than from
	// program start, so a boot can land in that half immediately.
	MOVWU	8(R2), R1	// CHI: upper 32 bits
	MOVWU	4(R2), R0	// CLO: lower 32 bits
	MOVWU	8(R2), R3	// re-read CHI
	CMP	R1, R3
	BNE	read_retry
	ORR	R1<<32, R0, R0
	MOVD	R0, ret+0(FP)
	RET

// func Busyloop(count uint32)
//
// Zero is checked before the first decrement, as the loop body decrements and
// then tests, so entering it with zero underflows and spins 2^32 times.
TEXT ·Busyloop(SB),$0-4
	MOVWU	count+0(FP), R0
	CBZ	R0, busyloop_done
busyloop:
	SUBW	$1, R0, R0
	CBNZ	R0, busyloop
busyloop_done:
	RET
