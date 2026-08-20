// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

// PSCI CPU_ON function ID, SMC64 calling convention, read by smp.s through
// go_asm.h.
const psciCPUOn = 0xc4000003

// defined in smp.s
func read_mpidr_el1() uint64
func sev()
func psci_cpu_on(targetCPU uint64, entry uint64, context uint64) int64

// ID returns the raw MPIDR_EL1 register value.
func (cpu *CPU) ID() uint64 {
	return read_mpidr_el1()
}

// SendEvent executes the SEV instruction to wake cores from WFE.
func (cpu *CPU) SendEvent() {
	sev()
}

// PSCICPUOn powers on a secondary CPU through the PSCI CPU_ON service.
func (cpu *CPU) PSCICPUOn(targetCPU uint64, entry uint64, context uint64) int64 {
	return psci_cpu_on(targetCPU, entry, context)
}
