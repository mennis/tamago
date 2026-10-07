// BCM2712 brcmstb PCIe root complex support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package pcie implements the BCM2712 brcmstb PCIe2 root complex
// initialization and RP1 southbridge enumeration.
//
// The firmware does not bring up the RP1 link, so RP1 peripherals at CPU
// 0x1f00000000 are unreachable until the root complex is initialized. The
// sequence follows the Linux pcie-brcmstb driver (brcm_pcie_setup,
// brcm_pcie_post_setup_bcm2712, brcm_pcie_start_link, brcm_pcie_map_bus).
//
// All MMIO goes through [RegIO] and all waits through an injected sleep
// function, so the sequence can be tested on the host.
package pcie

import (
	"fmt"
	"math/bits"
	"time"
)

// RegIO represents 32-bit MMIO access at absolute CPU addresses.
type RegIO interface {
	Read32(addr uint64) uint32
	Write32(addr uint64, val uint32)
}

// InboundWindow describes one PCIe to CPU (DMA) decode, mapping Size bytes at
// PCIOffset to system RAM at CPUAddr, as given by the device tree dma-ranges.
// Size must be a power of two in [4 KiB, 64 GiB], PCIOffset must be Size
// aligned and CPUAddr 4 KiB aligned.
type InboundWindow struct {
	Size      uint64
	PCIOffset uint64 // device (PCIe bus) address
	CPUAddr   uint64 // system RAM (CPU physical) address
}

// brcmstb PCIe2 register offsets, named after the Linux pcie-brcmstb driver.
const (
	regHardDebug    = 0x4304 // PCIE_MISC_HARD_PCIE_HARD_DEBUG (SERDES_IDDQ)
	regMiscCtrl     = 0x4008 // PCIE_MISC_MISC_CTRL
	regWin0Lo       = 0x400c // CPU_2_PCIE_MEM_WIN0_LO (PCI target addr, low)
	regWin0Hi       = 0x4010 // CPU_2_PCIE_MEM_WIN0_HI (PCI target addr, high)
	regWin0BaseLim  = 0x4070 // CPU_2_PCIE_MEM_WIN0_BASE_LIMIT
	regWin0BaseHi   = 0x4080 // CPU_2_PCIE_MEM_WIN0_BASE_HI
	regWin0LimitHi  = 0x4084 // CPU_2_PCIE_MEM_WIN0_LIMIT_HI
	regPriv1IDVal3  = 0x043c // PCIE_RC_CFG_PRIV1_ID_VAL3 (class code)
	regPriv1LinkCap = 0x04dc // PCIE_RC_CFG_PRIV1_LINK_CAPABILITY
	regUbusCtrl     = 0x40a4 // PCIE_MISC_UBUS_CTRL
	regUbusTimeout  = 0x40a8 // PCIE_MISC_UBUS_TIMEOUT
	regCfgRetryTO   = 0x405c // PCIE_MISC_RC_BAR2_CONFIG / retry timeout window
	regAxiReadErr   = 0x4170 // PCIE_MISC_AXI_READ_ERROR_DATA
	regAxiIntfCtrl  = 0x416c // PCIE_MISC_AXI_INTF_CTRL (BCM2712 QoS fixes)
	regPCIeCtrl     = 0x4064 // PERST control (bit PERSTB, deassert to train)
	regPCIeStatus   = 0x4068 // PCIE_MISC_PCIE_STATUS
	regPhyCtl15     = 0x184c // PCIE_RC_PL_PHY_CTL_15 (L1SS PM clock period)
	regMDIOAddr     = 0x1100 // PCIE_RC_DL_MDIO_ADDR (SERDES MDIO address/command)
	regMDIOWrData   = 0x1104 // PCIE_RC_DL_MDIO_WR_DATA (write data + DONE)
	regExtCfgIndex  = 0x9000 // downstream config index (bus<<20)|(devfn<<12)
	regExtCfgData   = 0x8000 // downstream config data window

	// Inbound windows: RC_BAR1-3 start at 0x402c, RC_BAR4+ at 0x40d4, each a
	// LO/HI pair. On BCM2712 each window also needs a UBUS BAR remap pair to
	// reach the memory controller.
	regRCBar1Lo      = 0x402c // PCIE_MISC_RC_BAR1_CONFIG_LO (size[4:0] | pci_offset[31:0])
	regRCBar4Lo      = 0x40d4 // PCIE_MISC_RC_BAR4_CONFIG_LO
	regUbusBar1Remap = 0x40ac // PCIE_MISC_UBUS_BAR1_CONFIG_REMAP (cpu_addr | ACCESS_EN)
	regUbusBar4Remap = 0x410c // PCIE_MISC_UBUS_BAR4_CONFIG_REMAP
)

// PCI configuration space offsets. The root port header is at the controller
// base, the endpoint is reached through the EXT_CFG_INDEX/DATA window (bus 1).
const (
	cfgVendorDevice = 0x00 // vendor (low 16) / device (high 16)
	cfgCommand      = 0x04 // PCI_COMMAND (low 16) / status (high 16)
	cfgBusNumbers   = 0x18 // primary/secondary/subordinate/latency
	cfgMemBaseLimit = 0x20 // Type-1 non-prefetchable memory base/limit
	cfgBAR0         = 0x10
	cfgBAR1         = 0x14
	cfgBAR2         = 0x18 // Type-0 endpoint only
)

// Register field values.
const (
	serdesIDDQ = 0x08000000 // HARD_DEBUG: SERDES power-down, cleared to enable

	perstB = 0x00000004 // PCIe control: deassert PERST

	statusPHYLINKUP = 0x00000010 // PCIE_STATUS: PHY link up
	statusDLACTIVE  = 0x00000020 // PCIE_STATUS: data-link layer active
	statusPort      = 0x00000080 // PCIE_STATUS: configured as root complex
	statusLinkReady = statusPHYLINKUP | statusDLACTIVE
	statusReady     = statusPHYLINKUP | statusDLACTIVE | statusPort

	cmdMemory    = 0x0002 // PCI_COMMAND: Memory Space Enable
	cmdBusMaster = 0x0004 // PCI_COMMAND: Bus Master Enable
	// cmdLowMask selects PCI_COMMAND; the upper half of the dword is
	// PCI_STATUS (W1C), which must be written as zero.
	cmdLowMask    = 0x0000ffff
	ubusErrSuppr  = 0x00082000 // UBUS_CTRL bits 13,19: suppress AXI err/DECERR
	axiReadErrVal = 0xffffffff // production read-failure substitution data

	rcBarSizeMask     = 0x1f // RC_BARn_CONFIG_LO size field [4:0] (encoded log2)
	ubusRemapAccessEn = 0x1  // UBUS_BARn_CONFIG_REMAP: enable this inbound decode

	// PCI BAR type bits [3:0]: I/O, memory type (00 32-bit, 10 64-bit) and
	// prefetchable.
	barIOSpace  = 0x1
	barTypeMask = 0x6
	barType64   = 0x4
	barPrefetch = 0x8

	miscCtrlConfig = 0x00203480 // Linux SCB/CFG-read-UR/burst/RCB/MPS fields
	miscCtrlMask   = 0x00303480 // fields modified by RMW, others preserved
	classBridge    = 0x30060400 // PCI-PCI bridge class in PRIV1_ID_VAL3
	linkCapVal     = 0x00315a42 // ASPM / link capability
	busPri0Sec1    = 0x00010100 // primary 0, secondary 1, subordinate 1

	// UBUS (~250 ms) and config retry (~240 ms) timeouts in 750 MHz clocks,
	// per brcm_pcie_post_setup_bcm2712.
	ubusTimeoutVal = 0x0b2d0000 // PCIE_MISC_UBUS_TIMEOUT
	cfgRetryTOVal  = 0x0aba0000 // PCIE_MISC config-retry timeout window

	// SERDES PHY setup (brcm_pcie_post_setup_bcm2712), required for the link
	// to train from a cold PHY.
	mdioCmdWrite  = 0x00000000 // MDIO command field: write (bit20 clear)
	mdioDataDone  = 0x80000000 // MDIO WR_DATA: transaction-done handshake bit
	mdioSetAddr   = 0x1f       // SET_ADDR_OFFSET: selects the SSC/refclk reg page
	mdioRefclkSel = 0x1600     // page value enabling a 54 MHz (xosc) refclk source

	phyCtl15PMClkMask = 0x000000ff // PCIE_RC_PL_PHY_CTL_15 PM_CLK_PERIOD field
	phyCtl15PMClk54M  = 0x12       // 18.52 ns (1/54 MHz, rounded down)

	// PCIE_MISC_AXI_INTF_CTRL bits (brcm_pcie_post_setup_bcm2712 QoS fixes).
	axiReqfifoQosProp   = 1 << 7  // AXI_REQFIFO_EN_QOS_PROPAGATION (clear: broken)
	axiRclkQosArrayFix  = 1 << 13 // AXI_EN_RCLK_QOS_ARRAY_FIX
	axiQosUpdTimingFix  = 1 << 12 // AXI_EN_QOS_UPDATE_TIMING_FIX
	axiDisQosGateMaster = 1 << 11 // AXI_DIS_QOS_GATING_IN_MASTER
	axiMaxOutstandMask  = 0x3f    // AXI_MASTER_MAX_OUTSTANDING_REQUESTS
	axiMaxOutstandThrot = 15      // throttle value when the timing fix is absent
	axiQosSet           = axiRclkQosArrayFix | axiQosUpdTimingFix | axiDisQosGateMaster
)

// rp1RefclkRegs holds the 54 MHz reference clock SERDES tuning, written via
// MDIO after selecting the refclk page, as in brcm_pcie_post_setup_bcm2712.
var rp1RefclkRegs = [...]struct{ reg, val uint16 }{
	{0x16, 0x50b9}, {0x17, 0xbda1}, {0x18, 0x0094}, {0x19, 0x97b4},
	{0x1b, 0x5030}, {0x1c, 0x5030}, {0x1e, 0x0007},
}

// RP1 endpoint identity and BAR layout (Pi 5 DT / drivers/mfd/rp1.c).
const (
	RP1VendorDevice = 0x00011de4 // 1de4:0001

	// 32-bit BAR sizes
	bar0Size = 16 << 10 // MSI-X table/PBA
	bar1Size = 4 << 20  // peripherals
	bar2Size = 64 << 10 // shared SRAM
	bar0Mask = 0xffffc000
	bar1Mask = 0xffc00000
	bar2Mask = 0xffff0000

	// BAR1 is placed at PCI address 0, so that the outbound window base
	// (0x1f00000000) maps to RP1 BAR1 offset 0.
	bar1PCI = 0x00000000
	bar2PCI = 0x00400000
	bar0PCI = 0x00410000

	// Root port Type-1 non-prefetchable window, PCI 0x000000-0x4fffff
	// (base[31:20]<<4 in [15:0], limit[31:20]<<4 in [31:16]).
	memBaseLimit = 0x00400000

	// RP1ChipID is the RP1 SYSINFO chip ID at BAR1 offset 0.
	RP1ChipID = 0x20001927
)

// Bounded poll parameters, each poll waits pollInterval between status reads.
const (
	perstSettle  = 100 * time.Millisecond // SERDES/PERST settling
	linkWait     = 100 * time.Millisecond // PCIe-required post-PERST delay
	pollInterval = 1 * time.Millisecond
	linkMaxPolls = 2000 // ~2 s deadline for PHYLINKUP|DL_ACTIVE
	cfgMaxPolls  = 1000 // ~10 s (at 10 ms) for RP1 config to answer
	cfgPollWait  = 10 * time.Millisecond

	mdioSettle   = 100 * time.Microsecond // after the refclk MDIO writes
	mdioPollWait = 10 * time.Microsecond
	mdioMaxPolls = 100 // ~1 ms deadline for an MDIO transaction to complete
)

// Stage identifies the bring-up step at which an error occurred.
type Stage int

const (
	StagePortCheck Stage = iota
	StagePHYSetup
	StageLinkTimeout
	StageEndpointNotFound
	StageBARLayout
	StageInbound
	StageRootMSE
	StageReady
)

func (s Stage) String() string {
	switch s {
	case StagePortCheck:
		return "port-check"
	case StagePHYSetup:
		return "phy-setup"
	case StageLinkTimeout:
		return "link-timeout"
	case StageEndpointNotFound:
		return "endpoint-not-found"
	case StageBARLayout:
		return "bar-layout"
	case StageInbound:
		return "inbound-window"
	case StageRootMSE:
		return "root-mse"
	case StageReady:
		return "ready-check"
	}
	return "unknown"
}

// Error carries the failing stage and the relevant register readback.
type Error struct {
	Stage Stage
	Msg   string
	Value uint32 // the status/register value that motivated the failure
}

func (e *Error) Error() string {
	return fmt.Sprintf("pcie: %s: %s (reg=%#08x)", e.Stage, e.Msg, e.Value)
}

// Controller drives one BCM2712 root port and its single RP1 endpoint.
type Controller struct {
	io      RegIO
	sleep   func(time.Duration)
	barrier func() // full data synchronization barrier (DSB SY on hardware)
	rcBase  uint64 // brcmstb PCIe2 controller base (0x10_00120000)
	cpuBase uint64 // outbound window CPU base (0x1f_00000000), RP1 BAR1 offset 0

	// inbound DMA windows, Bus Master is enabled only when non-empty
	inbound []InboundWindow

	// done is not synchronized, Init must be called from a single goroutine.
	done bool
}

// EnableDMA sets the inbound DMA windows programmed by [Controller.Init]. It
// must be called before Init, without windows Bus Master stays disabled.
func (c *Controller) EnableDMA(windows []InboundWindow) {
	c.inbound = windows
}

// dmaEnabled reports whether inbound DMA windows were configured.
func (c *Controller) dmaEnabled() bool { return len(c.inbound) > 0 }

// New returns a Controller for the brcmstb controller at rcBase with its
// outbound window at cpuBase. The barrier function performs a full data
// synchronization barrier, a nil barrier is a no-op.
func New(io RegIO, sleep func(time.Duration), barrier func(), rcBase, cpuBase uint64) *Controller {
	if barrier == nil {
		barrier = func() {}
	}
	return &Controller{io: io, sleep: sleep, barrier: barrier, rcBase: rcBase, cpuBase: cpuBase}
}

func (c *Controller) rd(off uint64) uint32    { return c.io.Read32(c.rcBase + off) }
func (c *Controller) wr(off uint64, v uint32) { c.io.Write32(c.rcBase+off, v) }
func (c *Controller) rmw(off uint64, set uint32) {
	c.wr(off, c.rd(off)|set)
}

// linkActive reports whether PHYLINKUP and DL_ACTIVE are set, downstream
// accesses are only valid while they are.
func (c *Controller) linkActive() bool {
	return c.rd(regPCIeStatus)&statusLinkReady == statusLinkReady
}

// epRead and epWrite access endpoint (bus 1, device 0, function 0)
// configuration space. They fail with the link down, as a downstream config
// cycle can then abort the CPU.
func (c *Controller) epRead(off uint64) (uint32, error) {
	if !c.linkActive() {
		return 0, &Error{StageLinkTimeout, "downstream config read with link down", c.rd(regPCIeStatus)}
	}
	c.wr(regExtCfgIndex, 1<<20)
	return c.rd(regExtCfgData + off), nil
}

func (c *Controller) epWrite(off uint64, v uint32) error {
	if !c.linkActive() {
		return &Error{StageLinkTimeout, "downstream config write with link down", c.rd(regPCIeStatus)}
	}
	c.wr(regExtCfgIndex, 1<<20)
	c.wr(regExtCfgData+off, v)
	return nil
}

// barRead32 and barWrite32 access endpoint BAR space at an absolute CPU
// address. They fail with the link down, where accesses silently return
// garbage. The caller is responsible for validating addr.
func (c *Controller) barRead32(addr uint64) (uint32, error) {
	if !c.linkActive() {
		return 0, &Error{StageLinkTimeout, "BAR read with link down", c.rd(regPCIeStatus)}
	}
	return c.io.Read32(addr), nil
}

func (c *Controller) barWrite32(addr uint64, v uint32) error {
	if !c.linkActive() {
		return &Error{StageLinkTimeout, "BAR write with link down", c.rd(regPCIeStatus)}
	}
	c.io.Write32(addr, v)
	return nil
}

// mdioWrite performs a SERDES MDIO write on MDIO_PORT0 and waits for the DONE
// bit to clear.
func (c *Controller) mdioWrite(regad uint16, val uint16) error {
	// port 0, write command
	c.wr(regMDIOAddr, uint32(regad)|mdioCmdWrite)
	_ = c.rd(regMDIOAddr) // barrier read, per the driver
	c.wr(regMDIOWrData, mdioDataDone|uint32(val))

	for i := 0; i < mdioMaxPolls; i++ {
		if c.rd(regMDIOWrData)&mdioDataDone == 0 {
			return nil
		}
		c.sleep(mdioPollWait)
	}
	return &Error{StagePHYSetup,
		fmt.Sprintf("MDIO write reg %#x did not complete", regad), uint32(regad)}
}

// phySetup configures the 54 MHz SERDES reference clock, the L1SS PM clock
// period and the AXI QoS fixes (brcm_pcie_post_setup_bcm2712). It must run
// with PERST asserted, before link training.
func (c *Controller) phySetup() error {
	if err := c.mdioWrite(mdioSetAddr, mdioRefclkSel); err != nil {
		return err
	}
	for _, r := range rp1RefclkRegs {
		if err := c.mdioWrite(r.reg, r.val); err != nil {
			return err
		}
	}
	c.sleep(mdioSettle)

	// L1SS PM clock period for a 54 MHz refclk
	c.wr(regPhyCtl15, (c.rd(regPhyCtl15)&^phyCtl15PMClkMask)|phyCtl15PMClk54M)

	// disable broken QoS propagation, set the 2712D0 chicken bits
	v := (c.rd(regAxiIntfCtrl) &^ axiReqfifoQosProp) | axiQosSet
	c.wr(regAxiIntfCtrl, v)
	// throttle outstanding requests if the timing fix is absent (2712C1 or
	// single-lane RC)
	if c.rd(regAxiIntfCtrl)&axiQosUpdTimingFix == 0 {
		c.wr(regAxiIntfCtrl, (c.rd(regAxiIntfCtrl)&^axiMaxOutstandMask)|axiMaxOutstandThrot)
	}
	return nil
}

// assertPERST holds the endpoint in fundamental reset, PERSTB is active low.
func (c *Controller) assertPERST()   { c.wr(regPCIeCtrl, c.rd(regPCIeCtrl)&^perstB) }
func (c *Controller) deassertPERST() { c.wr(regPCIeCtrl, c.rd(regPCIeCtrl)|perstB) }

// Init initializes the root complex and the RP1 endpoint. Subsequent calls
// only re-validate readiness, without retraining the link.
func (c *Controller) Init() error {
	if c.done {
		return c.Ready()
	}

	// reconfigure from a known link-down state
	c.assertPERST()

	// Power up the SERDES and program MISC_CTRL, preserving the firmware
	// SCB size bits [31:24].
	c.wr(regHardDebug, c.rd(regHardDebug)&^serdesIDDQ)
	c.sleep(perstSettle)
	c.wr(regMiscCtrl, (c.rd(regMiscCtrl)&^miscCtrlMask)|miscCtrlConfig)

	c.wr(regPriv1IDVal3, classBridge)
	c.wr(regPriv1LinkCap, linkCapVal)

	// Outbound window, before releasing PERST: CPU cpuBase+N maps to PCI N.
	// Base/limit have MiB granularity, the high dwords hold CPU bits [39:32].
	c.wr(regWin0Lo, uint32(bar1PCI))
	c.wr(regWin0Hi, 0)
	c.wr(regWin0BaseLim, winBaseLimit(c.cpuBase))
	c.wr(regWin0BaseHi, uint32(c.cpuBase>>32))
	c.wr(regWin0LimitHi, uint32(c.cpuBase>>32))

	// Inbound DMA windows, before link start and before Bus Master is set.
	if c.dmaEnabled() {
		if err := c.programInboundWindows(); err != nil {
			return err
		}
	}

	// Suppress AXI errors so that failed or timed out downstream reads return
	// all-ones instead of aborting the CPU (brcm_pcie_post_setup_bcm2712).
	c.rmw(regUbusCtrl, ubusErrSuppr)
	c.wr(regAxiReadErr, axiReadErrVal)
	c.wr(regUbusTimeout, ubusTimeoutVal)
	c.wr(regCfgRetryTO, cfgRetryTOVal)

	if err := c.phySetup(); err != nil {
		return err
	}

	// all configuration writes must land before link start
	c.barrier()
	c.deassertPERST()
	c.sleep(linkWait)

	var st uint32
	linkUp := false
	for i := 0; i < linkMaxPolls; i++ {
		st = c.rd(regPCIeStatus)
		if st&statusLinkReady == statusLinkReady {
			linkUp = true
			break
		}
		c.sleep(pollInterval)
	}
	if !linkUp {
		return &Error{StageLinkTimeout, "link did not train (PHYLINKUP|DL_ACTIVE)", st}
	}
	if st&statusPort == 0 {
		return &Error{StagePortCheck, "not in root-complex mode after link-up", st}
	}

	// Root port bus numbers, written directly: offset 0x18 is BAR2 on the
	// endpoint and must not go through the EXT_CFG window.
	c.wr(cfgBusNumbers, busPri0Sec1)

	var vid uint32
	for i := 0; i < cfgMaxPolls; i++ {
		v, err := c.epRead(cfgVendorDevice)
		if err != nil {
			return err
		}
		vid = v
		if vid != 0xffffffff && vid != 0 {
			break
		}
		c.sleep(cfgPollWait)
	}
	if vid != RP1VendorDevice {
		return &Error{StageEndpointNotFound,
			fmt.Sprintf("expected RP1 %#08x on bus 1", uint32(RP1VendorDevice)), vid}
	}

	// size BARs with endpoint memory decode disabled
	epCmd, err := c.epRead(cfgCommand)
	if err != nil {
		return err
	}
	if err := c.epWrite(cfgCommand, epCmd&^cmdMemory&cmdLowMask); err != nil {
		return err
	}
	if err := c.sizeBAR(cfgBAR0, bar0Mask); err != nil {
		return err
	}
	if err := c.sizeBAR(cfgBAR1, bar1Mask); err != nil {
		return err
	}
	if err := c.sizeBAR(cfgBAR2, bar2Mask); err != nil {
		return err
	}

	// assign fixed PCI addresses
	if err := c.assignBAR(cfgBAR1, bar1PCI); err != nil {
		return err
	}
	if err := c.assignBAR(cfgBAR2, bar2PCI); err != nil {
		return err
	}
	if err := c.assignBAR(cfgBAR0, bar0PCI); err != nil {
		return err
	}

	c.wr(cfgMemBaseLimit, memBaseLimit)

	// Enable endpoint Memory Space. Bus Master is set only with inbound
	// windows, an inherited Bus Master bit is cleared otherwise.
	epNew := epCmd | cmdMemory
	if c.dmaEnabled() {
		epNew |= cmdBusMaster
	} else {
		epNew &^= cmdBusMaster
	}
	if err := c.epWrite(cfgCommand, epNew&cmdLowMask); err != nil {
		return err
	}

	// Root port Memory Space Enable must be set after link-up, as the link
	// reset clears PCI_COMMAND. Bus Master follows the endpoint.
	rootCmd := c.rd(cfgCommand) | cmdMemory
	if c.dmaEnabled() {
		rootCmd |= cmdBusMaster
	} else {
		rootCmd &^= cmdBusMaster
	}
	rootCmd &= cmdLowMask
	c.wr(cfgCommand, rootCmd)
	_ = c.rd(cfgCommand)
	c.barrier()

	c.done = true
	return c.Ready()
}

// sizeBAR sizes a BAR, restoring its value, and checks that it is a
// non-prefetchable 32-bit memory BAR with the expected size mask.
func (c *Controller) sizeBAR(off uint64, wantMask uint32) error {
	orig, err := c.epRead(off)
	if err != nil {
		return err
	}
	if err := c.epWrite(off, 0xffffffff); err != nil {
		return err
	}
	got, err := c.epRead(off)
	if err != nil {
		return err
	}
	if err := c.epWrite(off, orig); err != nil {
		return err
	}

	switch {
	case got&barIOSpace != 0:
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x is I/O space, want 32-bit memory", off), got}
	case got&barTypeMask == barType64:
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x is a 64-bit memory BAR, want 32-bit", off), got}
	case got&barTypeMask != 0:
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x has reserved memory type bits", off), got}
	case got&barPrefetch != 0:
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x is prefetchable, want non-prefetchable", off), got}
	}

	if mask := got &^ 0xf; mask != wantMask {
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x size mask mismatch (want %#08x)", off, wantMask), got}
	}
	return nil
}

// assignBAR writes a PCI address to an endpoint BAR and verifies it reads back.
func (c *Controller) assignBAR(off uint64, addr uint32) error {
	if err := c.epWrite(off, addr); err != nil {
		return err
	}
	got, err := c.epRead(off)
	if err != nil {
		return err
	}
	if got&^0xf != addr {
		return &Error{StageBARLayout,
			fmt.Sprintf("BAR %#x did not accept assignment %#08x", off, addr), got}
	}
	return nil
}

// encodeIBarSize encodes a power of two inbound window size for RC_BAR
// CONFIG_LO (brcm_pcie_encode_ibar_size): 4-32 KiB map to 0x1c-0x1f, 64 KiB-64
// GiB to log2-15, anything else to 0 (disabled).
func encodeIBarSize(size uint64) uint32 {
	if size == 0 {
		return 0
	}
	log2 := bits.Len64(size) - 1
	switch {
	case log2 >= 12 && log2 <= 15:
		return uint32(log2-12) + 0x1c
	case log2 >= 16 && log2 <= 36:
		return uint32(log2 - 15)
	}
	return 0
}

// maxInboundWindows is the BCM2712 inbound window count. Beyond it the
// RC_BAR and UBUS remap offsets collide with other registers (e.g.
// barRegOffset(11) is UBUS_BAR4_CONFIG_REMAP).
const maxInboundWindows = 10

// barRegOffset and ubusRegOffset return the RC_BAR and UBUS remap register
// offsets for a 1-based inbound BAR index.
func barRegOffset(bar int) uint64 {
	if bar <= 3 {
		return regRCBar1Lo + 8*uint64(bar-1)
	}
	return regRCBar4Lo + 8*uint64(bar-4)
}

func ubusRegOffset(bar int) uint64 {
	if bar <= 3 {
		return regUbusBar1Remap + 8*uint64(bar-1)
	}
	return regUbusBar4Remap + 8*uint64(bar-4)
}

// programInboundWindows programs the RC_BAR decode and UBUS BAR remap for
// each inbound window, starting at BAR1.
func (c *Controller) programInboundWindows() error {
	if len(c.inbound) > maxInboundWindows {
		return &Error{StageInbound,
			fmt.Sprintf("%d inbound windows exceeds the %d this controller has", len(c.inbound), maxInboundWindows), uint32(len(c.inbound))}
	}
	for i, w := range c.inbound {
		bar := i + 1
		switch {
		case w.Size == 0 || w.Size&(w.Size-1) != 0:
			return &Error{StageInbound,
				fmt.Sprintf("inbound window %d size %#x is not a power of two", bar, w.Size), uint32(w.Size)}
		case w.PCIOffset&(w.Size-1) != 0:
			return &Error{StageInbound,
				fmt.Sprintf("inbound window %d PCI offset %#x not aligned to size %#x", bar, w.PCIOffset, w.Size), uint32(w.PCIOffset)}
		case w.CPUAddr&0xfff != 0:
			// the UBUS remap low 12 bits hold ACCESS_EN
			return &Error{StageInbound,
				fmt.Sprintf("inbound window %d CPU address %#x not 4 KiB aligned", bar, w.CPUAddr), uint32(w.CPUAddr)}
		}
		sz := encodeIBarSize(w.Size)
		if sz == 0 {
			return &Error{StageInbound,
				fmt.Sprintf("inbound window %d size %#x out of encodable range", bar, w.Size), uint32(w.Size)}
		}

		// RC_BAR: PCI offset, encoded size in [4:0]
		off := barRegOffset(bar)
		c.wr(off, (uint32(w.PCIOffset)&^rcBarSizeMask)|(sz&rcBarSizeMask))
		c.wr(off+4, uint32(w.PCIOffset>>32))

		// UBUS remap: CPU address, enable flag in the low 12 bits
		uoff := ubusRegOffset(bar)
		c.wr(uoff, (uint32(w.CPUAddr)&^0xfff)|ubusRemapAccessEn)
		c.wr(uoff+4, uint32(w.CPUAddr>>32))
	}
	return nil
}

// Ready validates the path to RP1 BAR1: link state, root port window and
// Memory Space Enable, endpoint Memory Space Enable and the RP1 chip ID.
func (c *Controller) Ready() error {
	st := c.rd(regPCIeStatus)
	if st&statusReady != statusReady {
		return &Error{StageReady, "link/port not active", st}
	}
	if cmd := c.rd(cfgCommand); cmd&cmdMemory == 0 {
		return &Error{StageRootMSE, "root-port Memory Space Enable clear", cmd}
	}
	if mbl := c.rd(cfgMemBaseLimit); mbl != memBaseLimit {
		return &Error{StageReady, "root-port bridge window wrong", mbl}
	}
	epCmd, err := c.epRead(cfgCommand)
	if err != nil {
		return err
	}
	if epCmd&cmdMemory == 0 {
		return &Error{StageReady, "endpoint Memory Space Enable clear", epCmd}
	}
	if id := c.io.Read32(c.cpuBase); id != RP1ChipID {
		return &Error{StageReady,
			fmt.Sprintf("RP1 SYSINFO chip ID mismatch (want %#08x)", uint32(RP1ChipID)), id}
	}
	return nil
}

// ReadyDMA validates, in addition to [Controller.Ready], that inbound DMA is
// configured: root port and endpoint Bus Master and each RC_BAR size field. It
// does not check MSI-X.
func (c *Controller) ReadyDMA() error {
	if err := c.Ready(); err != nil {
		return err
	}
	if !c.dmaEnabled() {
		return &Error{StageInbound, "DMA not enabled (no inbound windows configured)", 0}
	}
	if cmd := c.rd(cfgCommand); cmd&cmdBusMaster == 0 {
		return &Error{StageInbound, "root-port Bus Master clear", cmd}
	}
	epCmd, err := c.epRead(cfgCommand)
	if err != nil {
		return err
	}
	if epCmd&cmdBusMaster == 0 {
		return &Error{StageInbound, "endpoint Bus Master clear", epCmd}
	}
	for i, w := range c.inbound {
		bar := i + 1
		if lo := c.rd(barRegOffset(bar)); lo&rcBarSizeMask != encodeIBarSize(w.Size) {
			return &Error{StageInbound,
				fmt.Sprintf("inbound window %d size field not programmed", bar), lo}
		}
	}
	return nil
}

// winBaseLimit encodes the WIN0 base/limit dword (base[31:20] in [15:4],
// limit[31:20] in [31:20]) for a window spanning the whole 4 GiB segment.
func winBaseLimit(cpuBase uint64) uint32 {
	base := uint32(cpuBase) >> 20
	const limit = 0xfff // inclusive
	return (limit << 20) | (base << 4)
}
