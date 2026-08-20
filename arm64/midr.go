// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

// defined in midr.s
func read_midr_el1() uint64

// MIDR_EL1 field offsets.
//
// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D12.2.87 MIDR_EL1, Main ID Register.
const (
	midrRevision    = 0  // [3:0]
	midrPartNum     = 4  // [15:4]
	midrVariant     = 20 // [23:20]
	midrImplementer = 24 // [31:24]
)

// MIDR_EL1 part numbers of the ARM cores this package can name.
const (
	PartCortexA53 = 0xd03
	PartCortexA72 = 0xd08
	PartCortexA76 = 0xd0b
)

// CPUID represents the processor identity reported by MIDR_EL1, the variant
// and revision name the silicon revision (ARM writes these rNpM).
type CPUID struct {
	// Implementer is the JEP106 code of the designer; 0x41 is ARM.
	Implementer uint8
	// PartNum identifies the core, e.g. PartCortexA76.
	PartNum uint16
	// Variant is the major revision (the N in rNpM).
	Variant uint8
	// Revision is the minor revision (the M in rNpM).
	Revision uint8
}

// Name returns the core name, when known to this package, otherwise the empty
// string.
func (id CPUID) Name() string {
	if id.Implementer != 0x41 {
		return ""
	}

	switch id.PartNum {
	case PartCortexA53:
		return "Cortex-A53"
	case PartCortexA72:
		return "Cortex-A72"
	case PartCortexA76:
		return "Cortex-A76"
	}

	return ""
}

// Identify returns the identity of the calling core.
func (cpu *CPU) Identify() (id CPUID) {
	midr := read_midr_el1()

	id.Implementer = uint8(midr >> midrImplementer)
	id.PartNum = uint16(midr>>midrPartNum) & 0xfff
	id.Variant = uint8(midr>>midrVariant) & 0xf
	id.Revision = uint8(midr>>midrRevision) & 0xf

	return
}
