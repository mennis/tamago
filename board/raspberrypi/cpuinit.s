// Raspberry Pi early CPU initialization
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

//go:build linkcpuinit

#include "textflag.h"

// cpuinit replaces the arm/init.s entry point when linked with linkcpuinit, it
// also maps RAM Normal with 1MB sections before the runtime starts, as
// exclusives on Strongly-ordered memory abort on a BCM2836 and the runtime
// uses atomics before any board hook. arm.InitMMU replaces the map in Hwinit1.
//
// The table sits at 0x8000, below the bcm2835 mailbox and the load address.
#define TABLE	$0x8000

// Section descriptors (ARMv7-A short format):
//   Normal: section, B, C, AP=0b11, TEX=0b001, S
//   Device: section, B, AP=0b11, XN
#define NORMAL	$0x11c0e
#define DEVICE	$0x00c16

TEXT cpuinit(SB),NOSPLIT|NOFRAME,$0
	// set stack pointer
	MOVW	runtime∕goos·RamStart(SB), R13
	MOVW	runtime∕goos·RamSize(SB), R1
	MOVW	runtime∕goos·RamStackOffset(SB), R2
	ADD	R1, R13
	SUB	R2, R13
	MOVW	R13, R3

	// detect HYP mode and switch to SVC if necessary
	WORD	$0xe10f0000	// mrs r0, CPSR
	AND	$0x1f, R0, R0	// get processor mode

	CMP	$0x1a, R0	// HYP mode
	B.NE	after_eret

	BIC	$0x1f, R0
	ORR	$0x1d3, R0	// AIF masked, SVC mode
	// PC reads 8 bytes ahead and three words follow, after_eret is at +8
	MOVW	$8(R15), R14	// add lr, pc, #8 (after_eret)
	WORD	$0xe16ff000	// msr SPSR_fsxc, r0
	WORD	$0xe12ef30e	// msr ELR_hyp, lr
	WORD	$0xe160006e	// eret

after_eret:
	// enter System Mode
	WORD	$0xe321f0df	// msr CPSR_c, 0xdf

	MOVW	R3, R13

	// R4 = table, R5 = section index, R6 = first Device section
	MOVW	TABLE, R4
	MOVW	·earlyPeripheralBase(SB), R6
	MOVW	R6>>20, R6
	MOVW	NORMAL, R8
	MOVW	DEVICE, R9
	MOVW	$0, R5
	MOVW	$4096, R0

	// R10 is g and R11 the assembler temporary
fill:
	CMP	R6, R5
	MOVW.LO	R8, R7
	MOVW.HS	R9, R7
	MOVW	R5<<20, R1
	ORR	R1, R7

	MOVW	R5<<2, R2
	ADD	R4, R2
	MOVW	R7, (R2)

	ADD	$1, R5
	CMP	R0, R5
	B.LO	fill

	// domain 0 as a client, so the AP bits above are what decides access
	MOVW	$1, R0
	MCR	15, 0, R0, C3, C0, 0

	// TTBR0, and TTBCR=0 so it covers the whole address space
	MOVW	TABLE, R0
	MCR	15, 0, R0, C2, C0, 0
	MOVW	$0, R0
	MCR	15, 0, R0, C2, C0, 2

	// invalidate state left by the firmware
	MOVW	$0, R0
	MCR	15, 0, R0, C7, C5, 0	// invalidate instruction cache
	MCR	15, 0, R0, C8, C7, 0	// invalidate unified TLB
	MCR	15, 0, R0, C7, C10, 4	// data synchronization barrier
	MCR	15, 0, R0, C7, C5, 4	// instruction synchronization barrier

	// enable the MMU only, the caches are enabled after invalidation in
	// Hwinit1. SCTLR.XP (bit 23) selects the ARMv6 extended page table format
	// and is reserved as one on ARMv7.
	MRC	15, 0, R0, C1, C0, 0
	ORR	$1, R0			// MMU
	ORR	$(1<<23), R0		// extended page tables
	MCR	15, 0, R0, C1, C0, 0

	MCR	15, 0, R0, C7, C5, 4	// instruction synchronization barrier

	B	_rt0_tamago_start(SB)
