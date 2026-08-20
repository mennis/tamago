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

// func cache_flush_data()
TEXT ·cache_flush_data(SB),$0
	DMB	SY
	MRS	CLIDR_EL1, R0			// read CLIDR
	LSR	$23, R0, R3			// move LoC into position
	ANDS	$(7<<1), R3, R3			// extract LoC*2 from CLIDR
	BEQ	finished			// if LoC is 0, nothing to invalidate
start_invalidate_levels:
	MOVD	$0, R10				// start at cache level 0
invalidate_levels:
	ADD	R10>>1, R10, R2			// work out 3x current cache level
	LSR	R2, R0, R1			// extract cache type bits from CLIDR
	AND	$7, R1, R1			// mask the bits for current cache only
	CMP	$2, R1				// see what cache we have at this level
	BLT	skip				// skip if no cache, or just I-cache
	MSR	R10, CSSELR_EL1			// select current cache level in CSSELR
	ISB	SY				// sync the new CSSELR and CCSIDR
	MRS	CCSIDR_EL1, R1			// read the new CCSIDR
	AND	$7, R1, R2			// extract the length of the cache lines
	ADD	$4, R2, R2			// add 4 (line length offset)
	MOVD	$0x3ff, R4
	AND	R1>>3, R4, R4			// find maximum way number
	CLZW	R4, R5				// find bit position of way size increment
	MOVD	$0x7fff, R7
	AND	R1>>13, R7, R7			// extract maximum set number
loop_ways:
	MOVD	R7, R9				// create working copy of max set
loop_sets:
	LSL	R5, R4, R6
	ORR	R6, R10, R11			// factor way and cache level into R11
	LSL	R2, R9, R6
	ORR	R6, R11, R11			// factor set number into R11
	DC	ISW, R11			// invalidate by set/way
	SUBS	$1, R9, R9			// decrement the set
	BGE	loop_sets
	SUBS	$1, R4, R4			// decrement the way
	BGE	loop_ways
skip:
	ADD	$2, R10, R10			// increment cache level
	CMP	R10, R3
	BGT	invalidate_levels
finished:
	MOVD	$0, R10				// switch back to cache level 0
	MSR	R10, CSSELR_EL1			// select current cache level in CSSELR
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
