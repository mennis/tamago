// BCM2712 SoC SMP support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

#include "textflag.h"
#include "go_asm.h"

#define SY $0b1111

// func read_vbar_el1() uint64
TEXT ·read_vbar_el1(SB),$0-8
	MRS	VBAR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func read_mair_el1() uint64
TEXT ·read_mair_el1(SB),$0-8
	MRS	MAIR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func read_tcr_el1() uint64
TEXT ·read_tcr_el1(SB),$0-8
	MRS	TCR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func read_ttbr0_el1() uint64
TEXT ·read_ttbr0_el1(SB),$0-8
	MRS	TTBR0_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// Secondary core entry point, passed to PSCI CPU_ON. The core arrives with no
// stack, MMU or vector table, sets up minimal EL1 state and waits for a task.

// scratch map layout from smp.go
#define SECONDARY_STACK_BASE $const_secondaryStackBase
#define SECONDARY_STACK_SIZE $const_secondaryStackSize
#define TASK_BASE $const_taskBase

TEXT ·secondaryEntry(SB),NOSPLIT|NOFRAME,$0
	// firmware enters secondaries at EL2
	MRS	CurrentEL, R5
	LSR	$2, R5, R5
	AND	$0b11, R5, R5
	CMP	$2, R5
	BEQ	from_el2
	B	·secondaryEntryEL1(SB)

from_el2:
	MOVD	$(1<<31), R0		// HCR_EL2.RW=1
	WORD	$0xd51c1100		// msr hcr_el2, x0
	ISB	SY

	MOVD	$3, R0			// enable EL1 physical timer access
	WORD	$0xd51ce100		// msr cnthctl_el2, x0

	MOVD	$0, R0
	// CNTVOFF_EL2 is S3_4_C14_C0_3, 0xd51ce040 (op2=2) is UNDEFINED on A76
	WORD	$0xd51ce060		// msr cntvoff_el2, x0

	MOVD	$0x3c5, R0		// EL1h, DAIF masked
	WORD	$0xd51c4000		// msr spsr_el2, x0

	MOVD	$·secondaryEntryEL1(SB), R0
	WORD	$0xd51c4020		// msr elr_el2, x0
	ISB	SY
	ERET

TEXT ·secondaryEntryEL1(SB),NOSPLIT|NOFRAME,$0
	// Enable FP/SIMD
	MRS	CPACR_EL1, R0
	ORR	$(3 << 20), R0
	MSR	R0, CPACR_EL1
	ISB	$1

	// Mirror the BSP EL1 MMU/cache configuration.
	MOVD	·mairEL1Addr(SB), R0
	MSR	R0, MAIR_EL1
	ISB	SY

	MOVD	·tcrEL1Value(SB), R0
	MSR	R0, TCR_EL1
	ISB	SY

	MOVD	·ttbr0EL1Addr(SB), R0
	MSR	R0, TTBR0_EL1
	DSB	SY
	ISB	SY
	TLBI	VMALLE1
	DSB	SY
	ISB	SY

	MRS	SCTLR_EL1, R0
	BIC	$1<<19, R0		// clear WXN bit
	ORR	$1<<12, R0		// enable I-cache
	ORR	$1<<2, R0		// enable D-cache
	ORR	$1<<0, R0		// enable MMU
	MSR	R0, SCTLR_EL1
	ISB	SY

	// Set vector table (same as BSP)
	MOVD	·vecTableAddr(SB), R0
	MSR	R0, VBAR_EL1

	// core ID from MPIDR_EL1 Aff1 (bits 15:8)
	MRS	MPIDR_EL1, R0
	LSR	$8, R0, R0
	AND	$0x3, R0, R0		// core ID (0-3)
	MOVD	R0, R5

	// Stack top: base + coreID * stackSize
	MOVD	SECONDARY_STACK_SIZE, R1
	MUL	R1, R0, R0
	MOVD	SECONDARY_STACK_BASE, R1
	ADD	R1, R0, R0
	MOVD	R0, RSP

	// task slot: TASK_BASE + coreID * 24
	LSL	$3, R5, R1
	LSL	$4, R5, R2
	ADD	R2, R1, R1
	MOVD	TASK_BASE, R3
	ADD	R1, R3, R3

	// coreArrived[coreID] = 1, store-release now that MMU and caches are
	// on, so the BSP atomic load observes it
	MOVD	$·coreArrived(SB), R6
	ADD	R5<<2, R6, R6
	MOVW	$1, R7
	STLRW	R7, (R6)

wait:
	// acquire load of sp, the readiness flag, orders the gp/pc loads after it
	LDAR	(R3), R0		// sp
	CBZ	R0, sleep

	MOVD	8(R3), R1		// gp (g)
	MOVD	16(R3), R2		// pc (fn)

	CBZ	R2, sleep

	// Clear the task slot
	MOVD	$0, R4
	MOVD	R4, (R3)
	MOVD	R4, 8(R3)
	MOVD	R4, 16(R3)

	// coreStarted[coreID]++, single writer so a plain increment published
	// with store-release suffices
	MOVD	$·coreStarted(SB), R6
	ADD	R5<<2, R6, R6
	MOVWU	(R6), R7
	ADDW	$1, R7, R7
	STLRW	R7, (R6)

	// Set stack and g register
	MOVD	R0, RSP
	MOVD	R1, g

	// Call the task function
	BL	(R2)

	// If the task returns, go back to waiting
	B	wait

sleep:
	WFE
	B	wait
