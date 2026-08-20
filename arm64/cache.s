// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

#include "arm64.h"
#include "go_asm.h"
#include "textflag.h"

// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D12.2.100 SCTLR_EL1, System Control Register (EL1)

// func cache_disable()
TEXT ·cache_disable(SB),$0
	MRS	SCTLR_EL1, R0
	BIC	$1<<12, R0	// disable I-cache
	BIC	$1<<2, R0	// disable D-cache
	MSR	R0, SCTLR_EL1
	ISB	SY
	RET

// func cache_enable()
TEXT ·cache_enable(SB),$0
	MRS	SCTLR_EL1, R0
	ORR	$1<<12, R0	// enable I-cache
	ORR	$1<<2, R0	// enable D-cache
	MSR	R0, SCTLR_EL1
	ISB	SY
	RET

// func dcache_invalidate_all()
//
// Invalidate every level of data or unified cache below the Level of Coherency
// by set/way. Set/way maintenance does not depend on the MMU and must run
// before the data cache is enabled, as lines left behind by firmware or by a
// previous OS otherwise shadow memory this image has already written.
TEXT ·dcache_invalidate_all(SB),NOSPLIT,$0
	DSB	SY
	MRS	CLIDR_EL1, R10
	LSR	$24, R10, R11
	AND	$7, R11, R11		// R11 = Level of Coherency
	CBZ	R11, inv_finished
	MOVD	$0, R0			// R0 = cache level (0-based)
inv_loop_level:
	LSL	$1, R0, R14
	ADD	R0, R14, R14		// R14 = 3 * level
	LSR	R14, R10, R12
	AND	$7, R12, R12		// R12 = cache type at this level
	CMP	$2, R12
	BLT	inv_skip		// < 2: no data or unified cache here
	LSL	$1, R0, R14		// R14 = level << 1 = CSSELR value (data cache)
	MSR	R14, CSSELR_EL1
	ISB	SY
	MRS	CCSIDR_EL1, R12
	AND	$7, R12, R2
	ADD	$4, R2, R2		// R2 = log2(line bytes) = set-field shift
	UBFX	$3, R12, $10, R3	// R3 = associativity - 1 (max way)
	CLZW	R3, R4			// R4 = way-field shift
	UBFX	$13, R12, $15, R5	// R5 = number of sets - 1 (max set)
inv_loop_way:
	MOVD	R5, R6			// R6 = set index, from max down to 0
inv_loop_set:
	LSL	R4, R3, R7		// R7 = way << way_shift
	ORR	R14, R7, R7		// R7 |= level << 1
	LSL	R2, R6, R8		// R8 = set << set_shift
	ORR	R8, R7, R9		// R9 = set/way/level operand
	WORD	$0xd5087649		// dc isw, x9
	SUBS	$1, R6, R6		// set--
	BGE	inv_loop_set
	SUBS	$1, R3, R3		// way--
	BGE	inv_loop_way
inv_skip:
	ADD	$1, R0, R0		// level++
	CMP	R11, R0
	BLT	inv_loop_level
inv_finished:
	DSB	SY
	ISB	SY
	RET

// The range operations take the line aligned base address and the number of
// consecutive lines to maintain, as computed by cachemath.LineSpan. The count
// is tested first so that an empty range maintains no line. The stride comes
// from the Go side through go_asm.h.

// func clean_data_cache_range(first, n uintptr)
TEXT ·clean_data_cache_range(SB),$0-16
	MOVD	first+0(FP), R0
	MOVD	n+8(FP), R1
	CBZ	R1, clean_done		// n == 0: touch no line
clean_loop:
	WORD	$0xd50b7a20	// dc cvac, x0
	ADD	$const_cacheLineSize, R0
	SUBS	$1, R1, R1
	BNE	clean_loop
clean_done:
	DSB	SY
	RET

// func invalidate_data_cache_range(first, n uintptr)
TEXT ·invalidate_data_cache_range(SB),$0-16
	MOVD	first+0(FP), R0
	MOVD	n+8(FP), R1
	CBZ	R1, invalidate_done	// n == 0: touch no line
invalidate_loop:
	WORD	$0xd5087620	// dc ivac, x0
	ADD	$const_cacheLineSize, R0
	SUBS	$1, R1, R1
	BNE	invalidate_loop
invalidate_done:
	DSB	SY
	RET

// func flush_data_cache_range(first, n uintptr)
TEXT ·flush_data_cache_range(SB),$0-16
	MOVD	first+0(FP), R0
	MOVD	n+8(FP), R1
	CBZ	R1, flush_done		// n == 0: touch no line
flush_loop:
	WORD	$0xd50b7e20	// dc civac, x0
	ADD	$const_cacheLineSize, R0
	SUBS	$1, R1, R1
	BNE	flush_loop
flush_done:
	DSB	SY
	RET

// func data_synchronization_barrier()
TEXT ·data_synchronization_barrier(SB),NOSPLIT,$0
	DSB	SY
	RET

// func read_ctr_el0() uint64
//
// CTR_EL0, Cache Type Register, decoded by cachemath.DminLineSize.
TEXT ·read_ctr_el0(SB),NOSPLIT,$0-8
	MRS	CTR_EL0, R0
	MOVD	R0, ret+0(FP)
	RET
