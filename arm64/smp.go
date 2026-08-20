// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

import "math"

// PSCI CPU_ON function ID, SMC64 calling convention, read by smp.s through
// go_asm.h.
const psciCPUOn = 0xc4000003

// defined in smp.s
func read_mpidr_el1() uint64
func sev()
func wfe()
func psci_cpu_on(targetCPU uint64, entry uint64, context uint64) int64

// ID returns the raw MPIDR_EL1 register value.
func (cpu *CPU) ID() uint64 {
	return read_mpidr_el1()
}

// SendEvent executes the SEV instruction to wake cores from WFE.
func (cpu *CPU) SendEvent() {
	sev()
}

// EventIdleGovernor is the idle time management function for SMP operation,
// it halts in WFE rather than WFI so that [CPU.Wake] resumes it, as parked Ms
// are woken through goos.Wake. An SEV racing the WFE is not lost.
func (cpu *CPU) EventIdleGovernor(pollUntil int64) {
	if pollUntil == math.MaxInt64 {
		wfe()
	}
}

// Wake resumes processors halted in [CPU.EventIdleGovernor]. SEV is not
// targeted, every core waiting in WFE resumes and re-checks its own state.
func (cpu *CPU) Wake(_ uint64) {
	sev()
}

// PSCICPUOn powers on a secondary CPU through the PSCI CPU_ON service.
func (cpu *CPU) PSCICPUOn(targetCPU uint64, entry uint64, context uint64) int64 {
	return psci_cpu_on(targetCPU, entry, context)
}
