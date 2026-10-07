// BCM2712 MIP0 MSI-X interrupt peripheral support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"errors"

	"github.com/usbarmory/tamago/soc/bcm2712/pcie"
)

// MIP0 (DT msi-controller@1000130000, brcm,bcm2712-mip) turns a vector number
// written to its doorbell by a PCIe master into GIC SPI mipMSIBase+vector.
const (
	mip0Base = 0x10_00130000
	// DT msi-ranges GIC_SPI 128, i.e. INTID 160
	mipMSIBase = 128 + 32
	mipNumMSI  = 64

	// MIP registers (irq-bcm2712-mip)
	mipINTRaise = 0x00
	// unused, edge configured vectors need no MIP latch clear
	mipINTClear     = 0x10
	mipINTCFGLHost  = 0x20 // 1 = edge
	mipINTCFGHHost  = 0x30
	mipINTMASKLHost = 0x40 // 0 = unmasked
	mipINTMASKHHost = 0x50
	mipINTMASKLVPU  = 0x60
	mipINTMASKHVPU  = 0x70
)

// InitMSI unmasks all MIP0 vectors for the host, masks them for the VPU and
// configures them edge-triggered. It must run after [InitGIC] and [InitRP1].
// Each GIC SPI stays disabled until [RegisterMSI].
func InitMSI() {
	Write32(mip0Base+mipINTMASKLHost, 0)
	Write32(mip0Base+mipINTMASKHHost, 0)
	Write32(mip0Base+mipINTMASKLVPU, 0xffffffff)
	Write32(mip0Base+mipINTMASKHVPU, 0xffffffff)
	Write32(mip0Base+mipINTCFGLHost, 0xffffffff)
	Write32(mip0Base+mipINTCFGHHost, 0xffffffff)
}

// MSIDoorbell is the PCIe bus address of the MIP0 doorbell. Only a PCIe master
// can write it, the CPU outbound window does not reach it.
const MSIDoorbell = 0xff_fffff000

// mipNumRP1 is the number of RP1 MSI-X vectors (DT-bindings/mfd/rp1.h), a
// vector beyond it would address unrelated BAR1 registers.
const mipNumRP1 = 61

// validVector reports whether vector indexes a real RP1 MSI-X source.
func validVector(vector int) bool { return vector >= 0 && vector < mipNumRP1 }

// RegisterMSI registers fn as the handler for an MSI vector and enables its
// GIC SPI. The SPI must be configured edge-triggered, as the MIP pulses it.
func RegisterMSI(vector int, fn func()) {
	if !validVector(vector) {
		return
	}
	id := mipMSIBase + vector
	GIC.SetInterruptConfig(id, true)
	RegisterInterrupt(id, fn)
}

// RP1 PCIE_CFG block (DT rp1 pcie@40108000), mapping each RP1 interrupt
// source to an MSI-X vector.
const (
	rp1PCIeCfgBase = 0x108000
	rp1MSIXCfg0    = rp1PCIeCfgBase + 0x008
	rp1INTSTATL    = rp1PCIeCfgBase + 0x108
	rp1INTSTATH    = rp1PCIeCfgBase + 0x10c

	// MSIX_CFG_N bits (RP1 datasheet §6.1 Table 89)
	rp1MSIXEnable = 1 << 0
	rp1MSIXTest   = 1 << 1 // ORed with the interrupt source
	rp1MSIXIACK   = 1 << 2 // self-clearing
	rp1MSIXIACKEn = 1 << 3
)

// rp1MSIXCfgAddr returns the CPU address of MSIX_CFG_N in the RP1 PCIE_CFG block.
func rp1MSIXCfgAddr(vector int) uint64 {
	return PeripheralAddress(rp1MSIXCfg0 + uint64(vector)*4)
}

// SetupRP1MSI programs the RP1 MSI-X table entry for vector to target the MIP0
// doorbell and registers fn for it, leaving the source disabled. It must run
// after [InitMSI] and [InitRP1].
func SetupRP1MSI(vector int, fn func()) error {
	if rp1Controller == nil {
		return errors.New("bcm2712: RP1 not initialized")
	}
	if !validVector(vector) {
		return errors.New("bcm2712: MSI vector out of range")
	}
	if err := rp1Controller.SetupMSIX(vector, MSIDoorbell, uint32(vector)); err != nil {
		return err
	}
	RegisterMSI(vector, fn)
	return nil
}

// EnableRP1MSI enables an RP1 interrupt source. With iack the vector masks
// itself after each MSI until [AckRP1MSI], which level sources (all but
// USBHOST0/1) require.
func EnableRP1MSI(vector int, iack bool) {
	if !validVector(vector) {
		return
	}
	v := uint32(rp1MSIXEnable)
	if iack {
		v |= rp1MSIXIACKEn
	}
	Write32(rp1MSIXCfgAddr(vector), v)
}

// AckRP1MSI acknowledges an IACK masked vector. A still asserted source
// raises a new MSI (RP1 datasheet §6.2), so clear its cause first.
func AckRP1MSI(vector int) {
	if !validVector(vector) {
		return
	}
	Write32(rp1MSIXCfgAddr(vector), Read32(rp1MSIXCfgAddr(vector))|rp1MSIXIACK)
}

// TriggerTestMSI asserts an RP1 vector set up with [SetupRP1MSI] through the
// MSIX_CFG TEST bit, which does not show in RP1 INTSTAT.
func TriggerTestMSI(vector int) {
	if !validVector(vector) {
		return
	}
	Write32(rp1MSIXCfgAddr(vector), rp1MSIXEnable|rp1MSIXTest)
}

// ClearTestMSI drops the TEST assertion for vector, leaving it enabled.
func ClearTestMSI(vector int) {
	if !validVector(vector) {
		return
	}
	Write32(rp1MSIXCfgAddr(vector), rp1MSIXEnable)
}

// DisableRP1MSI disables an RP1 MSI-X source and its GIC SPI.
func DisableRP1MSI(vector int) {
	if !validVector(vector) {
		return
	}
	Write32(rp1MSIXCfgAddr(vector), 0)
	GIC.DisableInterrupt(mipMSIBase + vector)
}

// RP1MSIXCfg returns the MSIX_CFG_N register for vector, or 0 for an invalid
// vector.
func RP1MSIXCfg(vector int) uint32 {
	if !validVector(vector) {
		return 0
	}
	return Read32(rp1MSIXCfgAddr(vector))
}

// RP1MSIXDiag returns the endpoint MSI-X capability and table entry for vector.
func RP1MSIXDiag(vector int) (pcie.MSIXDiag, error) {
	if !validVector(vector) {
		return pcie.MSIXDiag{}, errors.New("bcm2712: RP1 MSI-X vector out of range")
	}
	if rp1Controller == nil {
		return pcie.MSIXDiag{}, errors.New("bcm2712: RP1 not initialized")
	}
	return rp1Controller.MSIXDiag(vector)
}

// RP1INTStatus returns the RP1 raw interrupt status (INTSTATH:INTSTATL).
func RP1INTStatus() uint64 {
	lo := Read32(PeripheralAddress(rp1INTSTATL))
	hi := Read32(PeripheralAddress(rp1INTSTATH))
	return uint64(hi)<<32 | uint64(lo)
}
