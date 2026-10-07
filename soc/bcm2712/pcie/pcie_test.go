// BCM2712 brcmstb PCIe root complex tests
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pcie

import (
	"errors"
	"testing"
	"time"
)

const (
	testRCBase  = 0x10_00120000
	testCPUBase = 0x1f_00000000
)

// model is an in-memory BCM2712 root complex and RP1 endpoint. As on hardware,
// link-up clears the root port PCI_COMMAND.
type model struct {
	rc map[uint64]uint32 // RC registers and root port config space
	ep map[uint64]uint32 // endpoint config space

	idx       uint32          // last EXT_CFG_INDEX written
	barOnes   map[uint64]bool // BAR holding an all-ones sizing probe
	barSet    map[uint64]bool // BAR assigned an address
	sizeMask  map[uint64]uint32
	stickyBAR uint64 // BAR ignoring address assignment, if non-zero

	vendor uint32

	perstDone   bool
	perstCount  int // PERST deassertions
	linkToUp    int // status reads after PERST before link-up
	linkReads   int
	linkUp      bool
	forceNoLink bool // link never trains

	cfgReadWhileDown bool // endpoint config read with link down

	mdioWrites        []mdioWrite // SERDES MDIO writes, in order
	mdioStuck         bool        // MDIO DONE never clears
	mdioAfterDeassert bool        // MDIO write after PERST deassert

	// Outbound window: msix backs BAR0/BAR2, memOutOfAperture flags accesses
	// outside the 4 GiB window.
	msix             map[uint64]uint32
	memAccesses      int
	memOutOfAperture bool

	// dropLinkOnMemWrite forces the link down after the next BAR write,
	// forceLinkDown holds it down until cleared.
	dropLinkOnMemWrite bool
	forceLinkDown      bool
}

// outboundAperture is the outbound window size, accesses outside it fault on
// hardware.
const outboundAperture = 0x1_00000000 // 4 GiB

type mdioWrite struct{ reg, val uint16 }

func newModel() *model {
	return &model{
		rc:       map[uint64]uint32{},
		ep:       map[uint64]uint32{},
		barOnes:  map[uint64]bool{},
		barSet:   map[uint64]bool{},
		sizeMask: map[uint64]uint32{cfgBAR0: bar0Mask, cfgBAR1: bar1Mask, cfgBAR2: bar2Mask},
		vendor:   RP1VendorDevice,
		linkToUp: 3,
	}
}

// forwarding reports whether a CPU read of BAR1 would complete.
func (m *model) forwarding() bool {
	return m.rc[regWin0BaseHi] == uint32(testCPUBase>>32) && // outbound window
		m.rc[cfgCommand]&cmdMemory != 0 && // root-port MSE
		m.rc[cfgMemBaseLimit] == memBaseLimit && // bridge window
		m.ep[cfgCommand]&cmdMemory != 0 && // endpoint MSE
		m.barSet[cfgBAR1] && m.ep[cfgBAR1] == bar1PCI // BAR1 assigned to PCI 0
}

func (m *model) Read32(addr uint64) uint32 {
	if addr >= testCPUBase {
		return m.memRead(addr)
	}

	off := addr - testRCBase

	switch {
	case off == regPCIeStatus:
		if m.forceLinkDown {
			return 0
		}
		if m.perstDone && !m.forceNoLink {
			m.linkReads++
			if !m.linkUp && m.linkReads >= m.linkToUp {
				// link-up clears PCI_COMMAND, PCI_STATUS survives
				m.linkUp = true
				m.rc[cfgCommand] &= 0xffff0000
			}
		}
		if m.linkUp {
			return statusReady
		}
		return 0

	case off == regMDIOWrData:
		if m.mdioStuck {
			return mdioDataDone
		}
		return 0

	case off >= regExtCfgData && off < regExtCfgData+0x1000:
		epOff := off - regExtCfgData
		if m.idx != 1<<20 {
			return 0xffffffff
		}
		if !m.linkUp {
			// would abort the CPU on hardware
			m.cfgReadWhileDown = true
			return 0xffffffff
		}
		if epOff == cfgVendorDevice {
			return m.vendor
		}
		if m.barOnes[epOff] {
			return m.sizeMask[epOff]
		}
		return m.ep[epOff]

	default:
		return m.rc[off]
	}
}

func (m *model) Write32(addr uint64, val uint32) {
	if addr >= testCPUBase {
		m.memWrite(addr, val)
		return
	}

	off := addr - testRCBase

	switch {
	case off == regPCIeCtrl:
		if val&perstB != 0 && !m.perstDone {
			m.perstDone = true
			m.perstCount++
			m.linkReads = 0
		}

	case off == regMDIOWrData:
		m.mdioWrites = append(m.mdioWrites,
			mdioWrite{reg: uint16(m.rc[regMDIOAddr]), val: uint16(val)})
		if m.perstDone {
			m.mdioAfterDeassert = true
		}
		m.rc[off] = val

	case off == regExtCfgIndex:
		m.idx = val

	case off == cfgCommand:
		// PCI_COMMAND in [15:0], PCI_STATUS (W1C) in [31:16]
		cur := m.rc[cfgCommand]
		w1c := (val >> 16) & 0xffff
		status := ((cur >> 16) & 0xffff) &^ w1c
		m.rc[cfgCommand] = (status << 16) | (val & 0xffff)

	case off >= regExtCfgData && off < regExtCfgData+0x1000:
		epOff := off - regExtCfgData
		if m.idx != 1<<20 {
			return
		}
		if _, isBAR := m.sizeMask[epOff]; isBAR {
			if val == 0xffffffff {
				m.barOnes[epOff] = true
			} else {
				m.barOnes[epOff] = false
				if epOff == m.stickyBAR {
					return
				}
				m.barSet[epOff] = true
				m.ep[epOff] = val
			}
			return
		}
		if epOff == cfgCommand {
			// same PCI_COMMAND/PCI_STATUS split as the root port
			cur := m.ep[cfgCommand]
			w1c := (val >> 16) & 0xffff
			status := ((cur >> 16) & 0xffff) &^ w1c
			m.ep[cfgCommand] = (status << 16) | (val & 0xffff)
			return
		}
		m.ep[epOff] = val

	default:
		m.rc[off] = val
	}
}

// memRead and memWrite model the outbound window: BAR1 returns the chip ID
// when forwarding, BAR0 and BAR2 are backed by msix.
func (m *model) memRead(addr uint64) uint32 {
	m.recordMem(addr)
	if addr < testCPUBase+bar1Size {
		if m.forwarding() {
			if addr == testCPUBase {
				return RP1ChipID
			}
			return 0
		}
		return axiReadErrVal
	}
	return m.msix[addr]
}

func (m *model) memWrite(addr uint64, val uint32) {
	m.recordMem(addr)
	if m.dropLinkOnMemWrite {
		// the write lands, the readback fails
		m.forceLinkDown = true
	}
	if m.msix == nil {
		m.msix = map[uint64]uint32{}
	}
	m.msix[addr] = val
}

func (m *model) recordMem(addr uint64) {
	m.memAccesses++
	if addr < testCPUBase || addr >= testCPUBase+outboundAperture {
		m.memOutOfAperture = true
	}
}

// msixCapOff is the model MSI-X capability offset.
const msixCapOff = 0x40

func (m *model) enableMSIX(tableSize int, tbl uint32) {
	m.ep[cfgCapPtr] = uint32(msixCapOff)
	m.ep[msixCapOff] = uint32(capIDMSIX) | uint32((tableSize-1)&msixTableSize)<<16
	m.ep[msixCapOff+msixTableReg] = tbl
}

func newController(m *model) *Controller {
	return New(m, func(time.Duration) {}, nil, testRCBase, testCPUBase)
}

// newMSIXController initializes a controller, installs an MSI-X capability and
// resets the memory access counters.
func newMSIXController(t *testing.T, tableSize int, tbl uint32) (*model, *Controller) {
	t.Helper()
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.enableMSIX(tableSize, tbl)
	m.memAccesses = 0
	m.memOutOfAperture = false
	return m, c
}

func asStage(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *pcie.Error", err)
	}
	return e
}

func TestInitSuccess(t *testing.T) {
	m := newModel()
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := c.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if got := m.Read32(testCPUBase); got != RP1ChipID {
		t.Fatalf("BAR1 chip ID = %#08x, want %#08x", got, uint32(RP1ChipID))
	}
	if m.rc[cfgCommand]&cmdMemory == 0 {
		t.Fatal("root-port Memory Space Enable not set after Init")
	}
	// no Bus Master without inbound windows
	if m.ep[cfgCommand]&cmdBusMaster != 0 {
		t.Fatal("endpoint Bus Master enabled but no inbound mappings exist")
	}
	if m.rc[cfgCommand]&cmdBusMaster != 0 {
		t.Fatal("root-port Bus Master enabled but no inbound mappings exist")
	}
	if m.ep[cfgCommand]&cmdMemory == 0 {
		t.Fatal("endpoint Memory Space Enable not set after Init")
	}
}

// TestRootMSEOrdering checks that root port Memory Space Enable is set after
// link-up, which clears it.
func TestRootMSEOrdering(t *testing.T) {
	m := newModel()
	m.rc[cfgCommand] = cmdMemory | cmdBusMaster
	m.perstDone = true
	for i := 0; i < m.linkToUp; i++ {
		m.Read32(testRCBase + regPCIeStatus)
	}
	if m.rc[cfgCommand]&cmdMemory != 0 {
		t.Fatal("model did not clear root-port PCI_COMMAND on link-up")
	}

	m2 := newModel()
	c := newController(m2)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m2.rc[cfgCommand]&cmdMemory == 0 {
		t.Fatal("root-port MSE cleared — written too early")
	}
}

func TestLinkTimeout(t *testing.T) {
	m := newModel()
	m.forceNoLink = true
	c := newController(m)

	err := c.Init()
	if err == nil {
		t.Fatal("expected link timeout error")
	}
	e := asStage(t, err)
	if e.Stage != StageLinkTimeout {
		t.Fatalf("stage = %s, want link-timeout", e.Stage)
	}
	if m.cfgReadWhileDown {
		t.Fatal("controller issued a downstream config read while link-down")
	}
}

func TestEndpointNotFound(t *testing.T) {
	m := newModel()
	m.vendor = 0x0001aaaa
	c := newController(m)

	err := c.Init()
	e := asStage(t, err)
	if e.Stage != StageEndpointNotFound {
		t.Fatalf("stage = %s, want endpoint-not-found", e.Stage)
	}
	if e.Value != m.vendor {
		t.Fatalf("error value = %#08x, want the offending vendor %#08x", e.Value, m.vendor)
	}
}

func TestBARLayoutRejected(t *testing.T) {
	m := newModel()
	m.sizeMask[cfgBAR1] = 0xfff00000
	c := newController(m)

	err := c.Init()
	e := asStage(t, err)
	if e.Stage != StageBARLayout {
		t.Fatalf("stage = %s, want bar-layout", e.Stage)
	}
}

func TestMiscCtrlRMWPreservesReserved(t *testing.T) {
	m := newModel()
	// bits above and within the low 24 bits, outside miscCtrlMask
	const reservedHigh = 0xab000000
	const reservedLow = 0x00040001
	if reservedLow&miscCtrlMask != 0 {
		t.Fatalf("test fixture overlaps miscCtrlMask %#08x", uint32(miscCtrlMask))
	}
	m.rc[regMiscCtrl] = reservedHigh | reservedLow
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := m.rc[regMiscCtrl]
	if got&^miscCtrlMask != reservedHigh|reservedLow {
		t.Fatalf("MISC_CTRL reserved bits clobbered: got %#08x, want reserved %#08x",
			got, uint32(reservedHigh|reservedLow))
	}
	if got&miscCtrlMask != miscCtrlConfig {
		t.Fatalf("MISC_CTRL config field = %#08x, want %#08x", got&miscCtrlMask, uint32(miscCtrlConfig))
	}
}

func TestUbusCtrlRMWPreservesBits(t *testing.T) {
	m := newModel()
	const other = 0x00000045
	m.rc[regUbusCtrl] = other
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := m.rc[regUbusCtrl]
	if got&other != other {
		t.Fatalf("UBUS_CTRL lost pre-existing bits: %#08x", got)
	}
	if got&ubusErrSuppr != ubusErrSuppr {
		t.Fatalf("UBUS_CTRL err-suppress bits not set: %#08x", got)
	}
}

func TestAxiReadErrorDataProduction(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := m.rc[regAxiReadErr]; got != axiReadErrVal {
		t.Fatalf("AXI_READ_ERROR_DATA = %#08x, want production %#08x", got, uint32(axiReadErrVal))
	}
}

func TestIdempotentNoRetrain(t *testing.T) {
	m := newModel()
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if m.perstCount != 1 {
		t.Fatalf("perst deasserted %d times on first Init", m.perstCount)
	}
	if err := c.Init(); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if m.perstCount != 1 {
		t.Fatalf("second Init retrained the link (perstCount=%d)", m.perstCount)
	}
}

func TestReadyDetectsClearedRootMSE(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.rc[cfgCommand] = 0
	err := c.Ready()
	e := asStage(t, err)
	if e.Stage != StageRootMSE {
		t.Fatalf("stage = %s, want root-mse", e.Stage)
	}
}

// TestEndpointBusMasterCleared checks that an inherited endpoint Bus Master bit
// is cleared without inbound windows.
func TestEndpointBusMasterCleared(t *testing.T) {
	m := newModel()
	m.ep[cfgCommand] = cmdBusMaster
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.ep[cfgCommand]&cmdBusMaster != 0 {
		t.Fatalf("endpoint Bus Master not cleared: %#08x", m.ep[cfgCommand])
	}
	if m.ep[cfgCommand]&cmdMemory == 0 {
		t.Fatalf("endpoint Memory Space not enabled: %#08x", m.ep[cfgCommand])
	}
}

// TestConfigRefusedWhileLinkDown checks that endpoint config accesses fail,
// without issuing a cycle, while the link is down.
func TestConfigRefusedWhileLinkDown(t *testing.T) {
	m := newModel()
	c := newController(m)

	if _, err := c.epRead(cfgVendorDevice); err == nil {
		t.Fatal("epRead did not refuse a link-down config read")
	} else if asStage(t, err).Stage != StageLinkTimeout {
		t.Fatalf("epRead stage = %s, want link-timeout", asStage(t, err).Stage)
	}
	if err := c.epWrite(cfgBAR1, 0); err == nil {
		t.Fatal("epWrite did not refuse a link-down config write")
	}
	if m.cfgReadWhileDown {
		t.Fatal("a downstream config cycle reached the model with link down")
	}
}

// TestBARTypeRejected checks that sizing rejects I/O, 64-bit and prefetchable
// BARs whose size mask matches.
func TestBARTypeRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		mask uint32
	}{
		{"io-space", bar1Mask | barIOSpace},
		{"64-bit", bar1Mask | barType64},
		{"prefetchable", bar1Mask | barPrefetch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.sizeMask[cfgBAR1] = tc.mask
			c := newController(m)

			e := asStage(t, c.Init())
			if e.Stage != StageBARLayout {
				t.Fatalf("stage = %s, want bar-layout", e.Stage)
			}
		})
	}
}

// TestBARAssignmentReadBack checks that a BAR refusing its address is detected.
func TestBARAssignmentReadBack(t *testing.T) {
	m := newModel()
	m.stickyBAR = cfgBAR2
	c := newController(m)

	e := asStage(t, c.Init())
	if e.Stage != StageBARLayout {
		t.Fatalf("stage = %s, want bar-layout", e.Stage)
	}
}

// TestRootCommandPreservesStatusW1C checks that root port PCI_COMMAND writes
// do not clear PCI_STATUS W1C bits.
func TestRootCommandPreservesStatusW1C(t *testing.T) {
	const statusBit = 0x4000 << 16
	m := newModel()
	m.rc[cfgCommand] = statusBit
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.rc[cfgCommand]&statusBit == 0 {
		t.Fatalf("root-command write cleared a PCI_STATUS W1C bit: %#08x", m.rc[cfgCommand])
	}
	if m.rc[cfgCommand]&cmdMemory == 0 {
		t.Fatalf("root-port MSE not set: %#08x", m.rc[cfgCommand])
	}
}

// TestTimeoutHardeningProgrammed checks that the UBUS and config retry
// timeouts are programmed.
func TestTimeoutHardeningProgrammed(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := m.rc[regUbusTimeout]; got != ubusTimeoutVal {
		t.Fatalf("UBUS_TIMEOUT = %#08x, want %#08x", got, uint32(ubusTimeoutVal))
	}
	if got := m.rc[regCfgRetryTO]; got != cfgRetryTOVal {
		t.Fatalf("config-retry timeout = %#08x, want %#08x", got, uint32(cfgRetryTOVal))
	}
}

// TestEndpointCommandPreservesStatusW1C checks that endpoint PCI_COMMAND
// writes do not clear PCI_STATUS W1C bits.
func TestEndpointCommandPreservesStatusW1C(t *testing.T) {
	const statusBit = 0x4000 << 16
	m := newModel()
	m.ep[cfgCommand] = statusBit
	c := newController(m)

	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.ep[cfgCommand]&statusBit == 0 {
		t.Fatalf("endpoint command write cleared a PCI_STATUS W1C bit: %#08x", m.ep[cfgCommand])
	}
	if m.ep[cfgCommand]&cmdMemory == 0 {
		t.Fatalf("endpoint MSE not set: %#08x", m.ep[cfgCommand])
	}
	if m.ep[cfgCommand]&cmdBusMaster != 0 {
		t.Fatalf("endpoint Bus Master not clear: %#08x", m.ep[cfgCommand])
	}
}

// TestPHYSetupSequence checks the refclk MDIO sequence, the L1SS PM clock
// period and the AXI QoS fixes.
func TestPHYSetupSequence(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	want := []mdioWrite{{mdioSetAddr, mdioRefclkSel}}
	for _, r := range rp1RefclkRegs {
		want = append(want, mdioWrite{r.reg, r.val})
	}
	if len(m.mdioWrites) != len(want) {
		t.Fatalf("MDIO writes = %d, want %d: %v", len(m.mdioWrites), len(want), m.mdioWrites)
	}
	for i, w := range want {
		if m.mdioWrites[i] != w {
			t.Fatalf("MDIO write %d = {%#x,%#x}, want {%#x,%#x}",
				i, m.mdioWrites[i].reg, m.mdioWrites[i].val, w.reg, w.val)
		}
	}

	if got := m.rc[regPhyCtl15] & phyCtl15PMClkMask; got != phyCtl15PMClk54M {
		t.Fatalf("L1SS PM clock period = %#x, want %#x", got, uint32(phyCtl15PMClk54M))
	}
	axi := m.rc[regAxiIntfCtrl]
	if axi&axiQosSet != axiQosSet {
		t.Fatalf("AXI QoS fix bits not set: %#08x", axi)
	}
	if axi&axiReqfifoQosProp != 0 {
		t.Fatalf("AXI broken QoS-propagation bit not cleared: %#08x", axi)
	}
}

// TestPHYSetupBeforeLink checks that SERDES setup runs with PERST asserted.
func TestPHYSetupBeforeLink(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(m.mdioWrites) != 1+len(rp1RefclkRegs) {
		t.Fatalf("phySetup did not issue the full MDIO sequence: %d writes", len(m.mdioWrites))
	}
	if m.mdioAfterDeassert {
		t.Fatal("an MDIO/SERDES write happened after PERST deassert (races link training)")
	}
}

// TestPHYSetupTimeout checks that a stuck MDIO write fails before PERST is
// released.
func TestPHYSetupTimeout(t *testing.T) {
	m := newModel()
	m.mdioStuck = true
	c := newController(m)

	e := asStage(t, c.Init())
	if e.Stage != StagePHYSetup {
		t.Fatalf("stage = %s, want phy-setup", e.Stage)
	}
	if m.perstCount != 0 {
		t.Fatalf("link was released despite a PHY-setup failure (perstCount=%d)", m.perstCount)
	}
}

// 1 GiB inbound window (encoded size 0xf)
var testDMAWindow = InboundWindow{
	Size:      1 << 30,
	PCIOffset: 1 << 30,
	CPUAddr:   0,
}

func TestEncodeIBarSize(t *testing.T) {
	for _, tc := range []struct {
		size uint64
		want uint32
	}{
		{4 << 10, 0x1c}, {8 << 10, 0x1d}, {16 << 10, 0x1e}, {32 << 10, 0x1f},
		{64 << 10, 0x01}, {1 << 20, 0x05}, {1 << 30, 0x0f}, {4 << 30, 0x11},
		{64 << 30, 0x15},
		{0, 0}, {2 << 10, 0}, {128 << 30, 0}, // out of range
	} {
		if got := encodeIBarSize(tc.size); got != tc.want {
			t.Errorf("encodeIBarSize(%#x) = %#x, want %#x", tc.size, got, tc.want)
		}
	}
}

func TestBarRegOffsets(t *testing.T) {
	for _, tc := range []struct {
		bar      int
		rc, ubus uint64
	}{
		{1, 0x402c, 0x40ac}, {2, 0x4034, 0x40b4}, {3, 0x403c, 0x40bc},
		{4, 0x40d4, 0x410c}, {5, 0x40dc, 0x4114},
	} {
		if got := barRegOffset(tc.bar); got != tc.rc {
			t.Errorf("barRegOffset(%d) = %#x, want %#x", tc.bar, got, tc.rc)
		}
		if got := ubusRegOffset(tc.bar); got != tc.ubus {
			t.Errorf("ubusRegOffset(%d) = %#x, want %#x", tc.bar, got, tc.ubus)
		}
	}
}

func TestDMADisabledByDefault(t *testing.T) {
	m := newModel()
	c := newController(m)
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.ep[cfgCommand]&cmdBusMaster != 0 || m.rc[cfgCommand]&cmdBusMaster != 0 {
		t.Fatal("Bus Master set without inbound windows")
	}
	if m.rc[regRCBar1Lo] != 0 {
		t.Fatalf("inbound window programmed without DMA: %#x", m.rc[regRCBar1Lo])
	}
	if err := c.ReadyDMA(); err == nil {
		t.Fatal("ReadyDMA passed with DMA disabled")
	} else if asStage(t, err).Stage != StageInbound {
		t.Fatalf("ReadyDMA stage = %s, want inbound-window", asStage(t, err).Stage)
	}
}

func TestInboundDMAEnabled(t *testing.T) {
	m := newModel()
	c := newController(m)
	c.EnableDMA([]InboundWindow{testDMAWindow})
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	wantLo := (uint32(testDMAWindow.PCIOffset) &^ 0x1f) | 0x0f
	if got := m.rc[regRCBar1Lo]; got != wantLo {
		t.Fatalf("RC_BAR1_LO = %#08x, want %#08x", got, wantLo)
	}
	if got := m.rc[regRCBar1Lo+4]; got != uint32(testDMAWindow.PCIOffset>>32) {
		t.Fatalf("RC_BAR1_HI = %#08x, want %#08x", got, uint32(testDMAWindow.PCIOffset>>32))
	}
	wantUbus := (uint32(testDMAWindow.CPUAddr) &^ 0xfff) | ubusRemapAccessEn
	if got := m.rc[regUbusBar1Remap]; got != wantUbus {
		t.Fatalf("UBUS_BAR1_REMAP = %#08x, want %#08x", got, wantUbus)
	}
	if got := m.rc[regUbusBar1Remap+4]; got != uint32(testDMAWindow.CPUAddr>>32) {
		t.Fatalf("UBUS_BAR1_REMAP_HI = %#08x, want %#08x", got, uint32(testDMAWindow.CPUAddr>>32))
	}
	if m.ep[cfgCommand]&cmdBusMaster == 0 {
		t.Fatal("endpoint Bus Master not set with DMA enabled")
	}
	if m.rc[cfgCommand]&cmdBusMaster == 0 {
		t.Fatal("root-port Bus Master not set with DMA enabled")
	}
	if m.ep[cfgCommand]&cmdMemory == 0 || m.rc[cfgCommand]&cmdMemory == 0 {
		t.Fatal("Memory Space Enable lost")
	}
	if err := c.ReadyDMA(); err != nil {
		t.Fatalf("ReadyDMA: %v", err)
	}
	if err := c.Ready(); err != nil {
		t.Fatalf("Ready (BAR1 path) regressed with DMA: %v", err)
	}
}

func TestInboundWindowBadSize(t *testing.T) {
	m := newModel()
	c := newController(m)
	c.EnableDMA([]InboundWindow{{Size: 0x30000, PCIOffset: 0, CPUAddr: 0}})
	e := asStage(t, c.Init())
	if e.Stage != StageInbound {
		t.Fatalf("stage = %s, want inbound-window", e.Stage)
	}
	if m.ep[cfgCommand]&cmdBusMaster != 0 {
		t.Fatal("Bus Master enabled despite a rejected inbound window")
	}
}

func TestInboundWindowMisaligned(t *testing.T) {
	m := newModel()
	c := newController(m)
	c.EnableDMA([]InboundWindow{{Size: 1 << 30, PCIOffset: 1 << 20, CPUAddr: 0}})
	e := asStage(t, c.Init())
	if e.Stage != StageInbound {
		t.Fatalf("stage = %s, want inbound-window", e.Stage)
	}
}

func TestReadyDMADetectsClearedBusMaster(t *testing.T) {
	m := newModel()
	c := newController(m)
	c.EnableDMA([]InboundWindow{testDMAWindow})
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.ep[cfgCommand] &^= cmdBusMaster
	if err := c.ReadyDMA(); err == nil {
		t.Fatal("ReadyDMA passed with endpoint Bus Master clear")
	} else if asStage(t, err).Stage != StageInbound {
		t.Fatalf("stage = %s, want inbound-window", asStage(t, err).Stage)
	}
}

// TestMSIXSetupAndDiag checks that a valid table entry is programmed and read
// back within the outbound window.
func TestMSIXSetupAndDiag(t *testing.T) {
	m, c := newMSIXController(t, 64, 0)
	if err := c.SetupMSIX(3, 0xff_fffff000, 3); err != nil {
		t.Fatalf("SetupMSIX: %v", err)
	}
	if m.memOutOfAperture {
		t.Fatal("SetupMSIX accessed outside the outbound aperture")
	}
	d, err := c.MSIXDiag(3)
	if err != nil {
		t.Fatalf("MSIXDiag: %v", err)
	}
	if d.Data != 3 {
		t.Fatalf("MSIXDiag Data = %#x, want 3", d.Data)
	}
	if d.EntryAddr != testCPUBase+bar0PCI+3*16 {
		t.Fatalf("MSIXDiag EntryAddr = %#x, want %#x", d.EntryAddr, uint64(testCPUBase+bar0PCI+3*16))
	}
}

// TestMSIXTableOffsetRejected checks that a table offset past its BAR is
// rejected before any table access.
func TestMSIXTableOffsetRejected(t *testing.T) {
	m, c := newMSIXController(t, 64, 0x00008000)
	e := asStage(t, c.SetupMSIX(0, 0xff_fffff000, 0))
	if e.Stage != StageBARLayout {
		t.Fatalf("stage = %s, want bar-layout", e.Stage)
	}
	if m.memAccesses != 0 {
		t.Fatalf("SetupMSIX issued %d memory-window accesses after a bad table offset; want 0", m.memAccesses)
	}
}

// TestMSIXTableOffsetGarbage checks that a corrupted Table dword with a valid
// BIR is rejected without accesses.
func TestMSIXTableOffsetGarbage(t *testing.T) {
	m, c := newMSIXController(t, 64, 0xfffffff8)
	e := asStage(t, c.SetupMSIX(0, 0xff_fffff000, 0))
	if e.Stage != StageBARLayout {
		t.Fatalf("stage = %s, want bar-layout", e.Stage)
	}
	if m.memOutOfAperture {
		t.Fatal("SetupMSIX formed an address outside the outbound aperture (would fault on hardware)")
	}
	if m.memAccesses != 0 {
		t.Fatalf("SetupMSIX issued %d memory-window accesses on a garbage table offset; want 0", m.memAccesses)
	}
}

// TestMSIXVectorRange checks that SetupMSIX and MSIXDiag reject out of range
// vectors.
func TestMSIXVectorRange(t *testing.T) {
	for _, v := range []int{-1, 64, 2048} {
		m, c := newMSIXController(t, 64, 0)
		if e := asStage(t, c.SetupMSIX(v, 0xff_fffff000, 0)); e.Stage != StageBARLayout {
			t.Fatalf("SetupMSIX(%d) stage = %s, want bar-layout", v, e.Stage)
		}
		if m.memAccesses != 0 {
			t.Fatalf("SetupMSIX(%d) issued %d memory accesses; want 0", v, m.memAccesses)
		}

		m2, c2 := newMSIXController(t, 64, 0)
		if _, err := c2.MSIXDiag(v); asStage(t, err).Stage != StageBARLayout {
			t.Fatalf("MSIXDiag(%d) stage = %s, want bar-layout", v, asStage(t, err).Stage)
		}
		if m2.memAccesses != 0 {
			t.Fatalf("MSIXDiag(%d) issued %d memory accesses; want 0", v, m2.memAccesses)
		}
	}
}

// TestMSIXFuncMaskRecovery checks that SetupMSIX clears an inherited Function
// Mask when MSI-X is already enabled.
func TestMSIXFuncMaskRecovery(t *testing.T) {
	m, c := newMSIXController(t, 64, 0)
	m.ep[msixCapOff] |= msixEnableDW | msixFuncMaskDW

	if err := c.SetupMSIX(0, 0xff_fffff000, 0); err != nil {
		t.Fatalf("SetupMSIX: %v", err)
	}
	ctrl := m.ep[msixCapOff]
	if ctrl&msixEnableDW == 0 {
		t.Fatalf("MSI-X Enable clear after SetupMSIX: %#08x", ctrl)
	}
	if ctrl&msixFuncMaskDW != 0 {
		t.Fatalf("Function Mask still set after SetupMSIX — every vector stays dead: %#08x", ctrl)
	}
}

// TestMSIXFirstEnableFailureThenRetry checks that a retry after a failed first
// SetupMSIX leaves the Function Mask clear.
func TestMSIXFirstEnableFailureThenRetry(t *testing.T) {
	m, c := newMSIXController(t, 64, 0)

	m.dropLinkOnMemWrite = true
	if err := c.SetupMSIX(0, 0xff_fffff000, 0); err == nil {
		t.Fatal("expected SetupMSIX to fail on the mid-table link drop")
	} else if asStage(t, err).Stage != StageLinkTimeout {
		t.Fatalf("stage = %s, want link-timeout", asStage(t, err).Stage)
	}
	if got := m.ep[msixCapOff] & (msixEnableDW | msixFuncMaskDW); got != msixEnableDW|msixFuncMaskDW {
		t.Fatalf("precondition not modeled: cap enable/mask bits = %#08x", got)
	}

	m.dropLinkOnMemWrite = false
	m.forceLinkDown = false
	if err := c.SetupMSIX(0, 0xff_fffff000, 0); err != nil {
		t.Fatalf("retry SetupMSIX: %v", err)
	}
	if m.ep[msixCapOff]&msixFuncMaskDW != 0 {
		t.Fatalf("Function Mask still set after retry — silent interrupt loss: %#08x", m.ep[msixCapOff])
	}
	if m.ep[msixCapOff]&msixEnableDW == 0 {
		t.Fatalf("MSI-X Enable clear after retry: %#08x", m.ep[msixCapOff])
	}
}

// TestInboundWindowCountRejected checks that too many inbound windows are
// rejected before any is programmed.
func TestInboundWindowCountRejected(t *testing.T) {
	m := newModel()
	c := newController(m)
	wins := make([]InboundWindow, maxInboundWindows+1)
	for i := range wins {
		wins[i] = InboundWindow{Size: 1 << 12, PCIOffset: uint64(i) << 12, CPUAddr: 0}
	}
	c.EnableDMA(wins)
	if e := asStage(t, c.Init()); e.Stage != StageInbound {
		t.Fatalf("stage = %s, want inbound-window", e.Stage)
	}
	if m.rc[regRCBar1Lo] != 0 {
		t.Fatalf("inbound window programmed despite over-count: RC_BAR1_LO=%#x", m.rc[regRCBar1Lo])
	}
	if m.rc[regUbusBar4Remap] != 0 {
		t.Fatalf("over-count clobbered UBUS_BAR4_REMAP (0x410c): %#x", m.rc[regUbusBar4Remap])
	}
}

// TestInboundCPUAddrMisaligned checks that a CPU address not 4 KiB aligned is
// rejected.
func TestInboundCPUAddrMisaligned(t *testing.T) {
	m := newModel()
	c := newController(m)
	c.EnableDMA([]InboundWindow{{Size: 1 << 12, PCIOffset: 0, CPUAddr: 0x1001}})
	if e := asStage(t, c.Init()); e.Stage != StageInbound {
		t.Fatalf("stage = %s, want inbound-window", e.Stage)
	}
	if m.rc[regUbusBar1Remap] != 0 {
		t.Fatalf("misaligned CPU address was programmed: UBUS_BAR1_REMAP=%#x", m.rc[regUbusBar1Remap])
	}
}

// TestInboundWindowCollisionBoundary checks that register offsets past
// maxInboundWindows collide with other registers.
func TestInboundWindowCollisionBoundary(t *testing.T) {
	if got := barRegOffset(maxInboundWindows + 1); got != regUbusBar4Remap {
		t.Fatalf("barRegOffset(%d) = %#x; expected the UBUS_BAR4_REMAP collision %#x",
			maxInboundWindows+1, got, uint64(regUbusBar4Remap))
	}
	if got := ubusRegOffset(16); got != regAxiIntfCtrl {
		t.Fatalf("ubusRegOffset(16) = %#x; expected the AXI_INTF_CTRL collision %#x", got, uint64(regAxiIntfCtrl))
	}
}
