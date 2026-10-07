// BCM2712 PCIe endpoint MSI-X support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

// MSI-X capability and table layout (PCIe Base Specification), the RP1 table
// is in BAR0.
const (
	cfgCapPtr = 0x34 // capabilities pointer (low byte -> first capability)
	capIDMSIX = 0x11 // MSI-X capability ID

	// Message Control, in the high 16 bits of the capability first dword
	msixEnableDW   = 1 << 31 // MSI-X Enable
	msixFuncMaskDW = 1 << 30 // Function Mask (mask all vectors)
	msixTableSize  = 0x7ff   // Table Size - 1 (Message Control [10:0])

	msixTableReg = 0x04 // Table Offset/BIR: BIR in [2:0], offset in [31:3]
	msixBIRMask  = 0x7

	// MSI-X table entry (16 bytes).
	msixEntAddrLo  = 0x0
	msixEntAddrHi  = 0x4
	msixEntData    = 0x8
	msixEntVecCtrl = 0xc
	msixVecMask    = 1 // per-vector Mask bit in Vector Control
)

// barPCIBase returns the PCI address assigned to a BAR index.
func barPCIBase(bir uint32) (uint64, bool) {
	switch bir {
	case 0:
		return bar0PCI, true
	case 1:
		return bar1PCI, true
	case 2:
		return bar2PCI, true
	}
	return 0, false
}

// barSize returns the size of a BAR index.
func barSize(bir uint32) (uint64, bool) {
	switch bir {
	case 0:
		return bar0Size, true
	case 1:
		return bar1Size, true
	case 2:
		return bar2Size, true
	}
	return 0, false
}

// resolveMSIXEntry returns the CPU address of the table entry for vector,
// along with the raw Message Control and Table Offset/BIR dwords.
//
// The whole table is bounded against its BAR, as a corrupted (e.g. all-ones)
// Table dword would otherwise address memory outside the outbound window.
func (c *Controller) resolveMSIXEntry(msixCap uint64, vector int) (entry uint64, ctrl, tbl uint32, err error) {
	if ctrl, err = c.epRead(msixCap); err != nil {
		return
	}
	size := int((ctrl>>16)&msixTableSize) + 1
	if vector < 0 || vector >= size {
		err = &Error{StageBARLayout, "MSI-X vector out of table range", uint32(vector)}
		return
	}
	if tbl, err = c.epRead(msixCap + msixTableReg); err != nil {
		return
	}
	bir := tbl & msixBIRMask
	pciBase, ok := barPCIBase(bir)
	if !ok {
		err = &Error{StageBARLayout, "MSI-X table BIR unsupported", bir}
		return
	}
	barSz, _ := barSize(bir)
	off := uint64(tbl &^ uint32(msixBIRMask))
	if off+uint64(size)*16 > barSz {
		err = &Error{StageBARLayout, "MSI-X table offset past BAR", tbl}
		return
	}
	entry = c.cpuBase + pciBase + off + uint64(vector)*16
	return
}

// findMSIXCap returns the configuration space offset of the endpoint MSI-X
// capability.
func (c *Controller) findMSIXCap() (uint64, error) {
	ptr, err := c.epRead(cfgCapPtr)
	if err != nil {
		return 0, err
	}
	off := uint64(ptr & 0xfc)
	for i := 0; off != 0 && i < 48; i++ {
		dw, err := c.epRead(off)
		if err != nil {
			return 0, err
		}
		if dw&0xff == capIDMSIX {
			return off, nil
		}
		off = uint64((dw >> 8) & 0xfc)
	}
	return 0, &Error{StageEndpointNotFound, "MSI-X capability not found", ptr}
}

// SetupMSIX programs the endpoint MSI-X table entry for vector to write
// msgData to msgAddr, unmasks it and enables MSI-X. It must be called after
// [Controller.Init] and does not enable any endpoint interrupt source.
func (c *Controller) SetupMSIX(vector int, msgAddr uint64, msgData uint32) error {
	msixCap, err := c.findMSIXCap()
	if err != nil {
		return err
	}

	entry, ctrl, _, err := c.resolveMSIXEntry(msixCap, vector)
	if err != nil {
		return err
	}

	// The Function Mask gates all vectors, set it only on the first enable so
	// that already armed vectors keep delivering; later calls rely on the
	// per-vector mask.
	firstEnable := ctrl&msixEnableDW == 0

	// Clear the Function Mask whenever it is found set, including one left by
	// a failed earlier call or by firmware.
	clearFuncMask := firstEnable || ctrl&msixFuncMaskDW != 0

	// Each write is read back to flush the posted write and to validate it, a
	// failed access reads back as all-ones.
	c.barrier()
	if firstEnable {
		if err := c.epWrite(msixCap, ctrl|msixEnableDW|msixFuncMaskDW); err != nil {
			return err
		}
		if _, err := c.epRead(msixCap); err != nil {
			return err
		}
	}

	if err := c.barWrite32(entry+msixEntVecCtrl, msixVecMask); err != nil { // mask
		return err
	}
	if v, err := c.barRead32(entry + msixEntVecCtrl); err != nil {
		return err
	} else if v&msixVecMask == 0 {
		return &Error{StageBARLayout, "MSI-X entry vector-control did not mask", v}
	}

	// verify all message fields, a corrupted address misdirects the write
	writeVerify := func(off uint64, val uint32) error {
		if err := c.barWrite32(entry+off, val); err != nil {
			return err
		}
		if v, err := c.barRead32(entry + off); err != nil {
			return err
		} else if v != val {
			return &Error{StageBARLayout, "MSI-X table entry did not read back", v}
		}
		return nil
	}
	if err := writeVerify(msixEntAddrLo, uint32(msgAddr)); err != nil {
		return err
	}
	if err := writeVerify(msixEntAddrHi, uint32(msgAddr>>32)); err != nil {
		return err
	}
	if err := writeVerify(msixEntData, msgData); err != nil {
		return err
	}

	if err := c.barWrite32(entry+msixEntVecCtrl, 0); err != nil { // unmask
		return err
	}
	if v, err := c.barRead32(entry + msixEntVecCtrl); err != nil {
		return err
	} else if v&msixVecMask != 0 {
		return &Error{StageBARLayout, "MSI-X entry vector-control did not unmask", v}
	}

	if clearFuncMask {
		// clear the Function Mask (the low half is read-only) and verify it
		if err := c.epWrite(msixCap, (ctrl|msixEnableDW)&^uint32(msixFuncMaskDW)); err != nil {
			return err
		}
		if v, err := c.epRead(msixCap); err != nil {
			return err
		} else if v&msixFuncMaskDW != 0 {
			return &Error{StageBARLayout, "MSI-X function mask did not clear", v}
		}
	}
	return nil
}

// MSIXDiag represents the MSI-X capability and table entry state of a vector.
type MSIXDiag struct {
	Control   uint16 // Message Control (bit15 Enable, bit14 Function Mask)
	TableDW   uint32 // Table Offset/BIR
	EntryAddr uint64 // table entry CPU address
	AddrLo    uint32 // table entry
	AddrHi    uint32
	Data      uint32
	VecCtrl   uint32
}

// MSIXDiag reads back the MSI-X capability and table entry for vector,
// resolving the table as [Controller.SetupMSIX] does.
func (c *Controller) MSIXDiag(vector int) (MSIXDiag, error) {
	var d MSIXDiag
	msixCap, err := c.findMSIXCap()
	if err != nil {
		return d, err
	}
	entry, ctrl, tbl, err := c.resolveMSIXEntry(msixCap, vector)
	if err != nil {
		return d, err
	}
	d.Control = uint16(ctrl >> 16)
	d.TableDW = tbl
	d.EntryAddr = entry
	c.barrier()
	if d.AddrLo, err = c.barRead32(d.EntryAddr + msixEntAddrLo); err != nil {
		return d, err
	}
	if d.AddrHi, err = c.barRead32(d.EntryAddr + msixEntAddrHi); err != nil {
		return d, err
	}
	if d.Data, err = c.barRead32(d.EntryAddr + msixEntData); err != nil {
		return d, err
	}
	if d.VecCtrl, err = c.barRead32(d.EntryAddr + msixEntVecCtrl); err != nil {
		return d, err
	}
	return d, nil
}
