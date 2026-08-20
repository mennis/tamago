// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

#include "arm64.h"
#include "go_asm.h"

// func read_mpidr_el1() uint64
TEXT ·read_mpidr_el1(SB),$0-8
	MRS	MPIDR_EL1, R0
	MOVD	R0, ret+0(FP)
	RET

// func sev()
TEXT ·sev(SB),$0
	SEV
	RET

// func psci_cpu_on(targetCPU uint64, entry uint64, context uint64) int64
TEXT ·psci_cpu_on(SB),$0-32
	MOVD	targetCPU+0(FP), R1
	MOVD	entry+8(FP), R2
	MOVD	context+16(FP), R3
	MOVD	$const_psciCPUOn, R0
	WORD	$0xd4000003	// SMC #0
	MOVD	R0, ret+24(FP)
	RET
