// Cadence GEM (macb) Ethernet MAC driver for the Raspberry Pi 5 RP1
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package gem implements a driver for the Cadence GEM (Gigabit Ethernet MAC,
// "cdns,macb") as instantiated in the Raspberry Pi 5 RP1 southbridge
// (ethernet@100000), following the Linux macb driver (macb.h, rpi-6.6.y).
//
// Register access and DMA memory go through the injected [RegIO] and [DMA]
// interfaces, so the driver logic can be tested on the host. Descriptor rings
// live in DMA-coherent memory, frame buffers in cacheable memory with
// per-buffer cache maintenance. The PHY is driven separately (e.g.
// phy/broadcom/bcm54213) over the MDIO transport provided here.
//
// This package is only meant to be used with `GOOS=tamago` as supported by the
// TamaGo framework for bare metal Go, see https://github.com/usbarmory/tamago.
package gem

import (
	"errors"
	"net"
	"sync"
	"time"
)

// GEM registers (Linux macb.h)
const (
	NCR    = 0x000 // network control
	NCFGR  = 0x004 // network configuration
	NSR    = 0x008 // network status (MDIO idle)
	DMACFG = 0x010 // DMA configuration (GEM)
	TSR    = 0x014 // transmit status (W1C)
	RBQP   = 0x018 // receive buffer queue pointer (low)
	TBQP   = 0x01c // transmit buffer queue pointer (low)
	RSR    = 0x020 // receive status (W1C)
	ISR    = 0x024 // interrupt status (W1C)
	IER    = 0x028 // interrupt enable
	IDR    = 0x02c // interrupt disable
	IMR    = 0x030 // interrupt mask (read-only)
	MAN    = 0x034 // PHY maintenance (MDIO frame)
	SA1B   = 0x098 // specific address 1 bottom (octets 0-3)
	SA1T   = 0x09c // specific address 1 top (octets 4-5)
	TXCNT  = 0x108 // frames transmitted ok (GEM statistic)
	TXBCNT = 0x10c // broadcast frames transmitted (GEM statistic)
	RXCNT  = 0x158 // frames received ok (GEM statistic)
	TBQPH  = 0x4c8 // transmit buffer queue pointer high (GEM ADDR64)
	RBQPH  = 0x4d4 // receive buffer queue pointer high (GEM ADDR64)
)

// NCR (network control) bits.
const (
	ncrRE      = 1 << 2 // receive enable
	ncrTE      = 1 << 3 // transmit enable
	ncrMPE     = 1 << 4 // management port enable (MDIO)
	ncrCLRSTAT = 1 << 5 // clear statistics registers
	ncrTSTART  = 1 << 9 // start transmission
)

// NCFGR (network configuration) bits.
const (
	ncfgrSPD = 1 << 0  // speed: 100 Mbps (else 10)
	ncfgrFD  = 1 << 1  // full duplex
	ncfgrGBE = 1 << 10 // gigabit mode
	// MDC divider, NCFGR[20:18]: 5 selects pclk/96, keeping MDC below
	// 2.5 MHz for any plausible RP1 pclk.
	ncfgrMDCShift = 18
	ncfgrMDCMask  = 7 << ncfgrMDCShift
	ncfgrMDCDiv96 = 5 << ncfgrMDCShift
)

// DMACFG (GEM DMA configuration) bits/fields (macb.h).
const (
	dmacfgADDR64    = 1 << 30 // 64-bit descriptor addresses (ADDR64)
	dmacfgRXBSShift = 16      // receive buffer size, in 64-byte units [23:16]
	dmacfgFBLDO16   = 0x10    // AMBA fixed burst length of 16 [4:0]

	// RXBMS [9:8] and TXPBMS [10] are set to their maximum, as in Linux
	// macb_configure_dma, as the reset partition risks RX overruns and TX
	// underruns with full-size frames at 1000FD.
	dmacfgRXBMSFull = 3 << 8  // full RX packet buffer memory size
	dmacfgTXPBMS    = 1 << 10 // full TX packet buffer memory size
)

// NSR (network status) bits.
const nsrIDLE = 1 << 2 // MDIO logic idle

// TSR (transmit status, W1C) bits.
const (
	tsrUBR   = 1 << 0 // used bit read: TX queue exhausted (not an error)
	tsrRLE   = 1 << 2 // retry limit exceeded
	tsrTGO   = 1 << 3 // transmit go (active; live status, not W1C)
	tsrBEX   = 1 << 4 // buffers exhausted mid frame
	tsrCOMP  = 1 << 5 // transmit complete
	tsrUND   = 1 << 6 // transmit underrun
	tsrHRESP = 1 << 8 // AMBA error response on a DMA access

	tsrErrors = tsrRLE | tsrBEX | tsrUND | tsrHRESP
)

// RSR (receive status, W1C) bits.
const (
	rsrBNA   = 1 << 0 // buffer not available (RX queue exhausted; not an error)
	rsrREC   = 1 << 1 // frame received
	rsrOVR   = 1 << 2 // receive overrun
	rsrHRESP = 1 << 3 // AMBA error response on a DMA access

	rsrErrors = rsrOVR | rsrHRESP
)

// ISR/IER/IDR interrupt bits. ISR is not clear-on-read, asserted bits are
// acknowledged by writing them back (W1C).
const (
	intMFD   = 1 << 0  // management frame done
	intRCOMP = 1 << 1  // receive complete
	intRXUBR = 1 << 2  // receive used bit read (RX queue exhausted)
	intTXUBR = 1 << 3  // transmit used bit read
	intTUND  = 1 << 4  // transmit underrun
	intRLE   = 1 << 5  // retry limit exceeded
	intTXERR = 1 << 6  // transmit error (buffers exhausted etc.)
	intTCOMP = 1 << 7  // transmit complete
	intROVR  = 1 << 10 // receive overrun
	intHRESP = 1 << 11 // AMBA error response

	// interrupts armed by EnableInterrupts
	intDefault = intRCOMP | intTCOMP | intTXERR | intTUND | intRLE | intROVR | intHRESP
)

// MDIO clause-22 frame fields (MAN register): SOF=01[31:30], RW[29:28]
// (10=read, 01=write), PHYA[27:23], REGA[22:18], CODE=10[17:16], DATA[15:0].
const (
	manSOF   = 1 << 30
	manRead  = 2 << 28
	manWrite = 1 << 28
	manCode  = 2 << 16
)

// Frame sizing. The received length includes the FCS, and receive buffers are
// sized in DMACFG 64-byte units.
const (
	MTU          = 1500
	maxFrameSize = MTU + 14 + 4 // header + payload + FCS
	minFrameSize = 60           // minimum Ethernet frame the MAC will pad to
	fcsLen       = 4

	bufAlign = 64
	rxBufLen = (maxFrameSize + bufAlign - 1) &^ (bufAlign - 1) // 64-byte multiple

	defaultRingSize = 64

	// bound the NSR.IDLE poll, so that an absent PHY fails rather than hangs
	mdioPollInterval = 10 * time.Microsecond
	mdioPollCount    = 1000
)

// RegIO represents 32-bit MMIO access at absolute addresses.
type RegIO interface {
	Read32(addr uint64) uint32
	Write32(addr uint64, val uint32)
}

// DMA represents the DMA allocator, bus address translation and cache
// maintenance used by the GEM. All addresses are CPU-physical unless returned
// by Bus.
type DMA interface {
	// Reserve allocates a cacheable frame buffer, returning its CPU-physical
	// address and a slice aliasing it. Buffers are cache-line isolated.
	Reserve(size, align int) (addr uint, buf []byte)
	// ReserveCoherent allocates non-cacheable memory for a descriptor ring.
	// The 16-byte descriptors share cache lines with ones the GEM writes
	// concurrently, so a cache line writeback would clobber them; coherent
	// memory needs no cache maintenance, only Barrier ordering.
	ReserveCoherent(size, align int) (addr uint, buf []byte)
	// Release frees a region returned by Reserve or ReserveCoherent.
	Release(addr uint)
	// Bus translates a CPU-physical address to the GEM bus master address
	// (RP1: 0x10_00000000 + cpu, through the PCIe inbound window).
	Bus(cpu uint64) uint64
	// Clean writes back CPU cache lines over [addr, addr+size).
	Clean(addr uint, size int)
	// Invalidate discards CPU cache lines over [addr, addr+size).
	Invalidate(addr uint, size int)
	// Barrier issues a data synchronization barrier ordering MMIO against
	// DMA memory.
	Barrier()
}

// GEM represents a Cadence GEM Ethernet MAC instance.
type GEM struct {
	sync.Mutex

	// Base register (RP1: 0x1f_0010_0000)
	Base uint64
	// Register access
	Reg RegIO
	// DMA memory and cache maintenance
	DMA DMA

	// RingSize is the number of descriptors per ring (default 64).
	RingSize int
	// MAC is the station address, when nil Init sets a fixed locally
	// administered address (02:74:61:6d:61:67) which is not unique.
	MAC net.HardwareAddr

	// Speed (10, 100 or 1000) and FullDuplex hold the link parameters
	// resolved by the PHY (default 1000FD).
	Speed      int
	FullDuplex bool

	// TXHandler and RXHandler are invoked by HandleIRQ on transmit and
	// receive completion.
	TXHandler func()
	RXHandler func()

	tx ring
	rx ring

	started bool

	// mdioMu serializes MDIO transactions only, so that an MDIO poll timeout
	// does not stall the data path. MAN/NSR are not used by the data path and
	// NCR.MPE is set once at Init.
	mdioMu sync.Mutex
}

// r reads a GEM register by offset.
func (hw *GEM) r(off uint64) uint32 { return hw.Reg.Read32(hw.Base + off) }

// w writes a GEM register by offset.
func (hw *GEM) w(off uint64, val uint32) { hw.Reg.Write32(hw.Base+off, val) }

// Init initializes the GEM for MDIO and DMA operation, with the data path
// halted until [GEM.Start].
func (hw *GEM) Init() error {
	hw.Lock()
	defer hw.Unlock()

	if hw.Base == 0 || hw.Reg == nil || hw.DMA == nil {
		return errors.New("gem: invalid controller instance (Base/Reg/DMA required)")
	}

	if hw.MAC == nil {
		hw.MAC = make(net.HardwareAddr, 6)
		// locally administered, unicast
		hw.MAC[0] = 0x02
		copy(hw.MAC[1:], []byte{0x74, 0x61, 0x6d, 0x61, 0x67}) // "tamag"
	} else if len(hw.MAC) != 6 {
		return errors.New("gem: invalid MAC length")
	}

	if hw.RingSize <= 0 {
		hw.RingSize = defaultRingSize
	}
	if hw.Speed == 0 {
		hw.Speed = 1000
		hw.FullDuplex = true
	}

	// Halt the data path; the queue pointers are only writable while halted.
	hw.w(NCR, hw.r(NCR)&^uint32(ncrTE|ncrRE))

	// Clear statistics and disable+acknowledge all interrupts.
	hw.w(NCR, hw.r(NCR)|ncrCLRSTAT)
	hw.w(IDR, 0xffffffff)
	hw.w(ISR, hw.r(ISR)) // W1C the asserted set
	hw.w(TSR, 0x1ff)     // W1C through HRESP
	hw.w(RSR, 0xf)

	// Management port + MDC divider for MDIO.
	hw.w(NCFGR, (hw.r(NCFGR)&^uint32(ncfgrMDCMask))|ncfgrMDCDiv96)
	hw.w(NCR, hw.r(NCR)|ncrMPE)

	// 64-bit descriptors, as the RP1 bus address of system RAM is above 4GB
	hw.w(DMACFG, dmacfgADDR64|
		uint32(rxBufLen/bufAlign)<<dmacfgRXBSShift|
		dmacfgRXBMSFull|dmacfgTXPBMS|
		dmacfgFBLDO16)

	hw.applyLink()
	hw.setMAC()

	hw.DMA.Barrier()
	return nil
}

// applyLink writes the resolved speed/duplex into NCFGR. NCR must be halted
// around a speed change; callers (Init, SetLink) hold that invariant.
func (hw *GEM) applyLink() {
	cfg := hw.r(NCFGR) &^ uint32(ncfgrSPD|ncfgrGBE|ncfgrFD)

	switch hw.Speed {
	case 1000:
		cfg |= ncfgrGBE
	case 100:
		cfg |= ncfgrSPD
	case 10:
		// neither GBE nor SPD
	}
	if hw.FullDuplex {
		cfg |= ncfgrFD
	}

	hw.w(NCFGR, cfg)
}

// SetLink applies the link parameters resolved by the PHY, briefly halting
// the data path.
func (hw *GEM) SetLink(speed int, fullDuplex bool) {
	hw.Lock()
	defer hw.Unlock()

	hw.Speed = speed
	hw.FullDuplex = fullDuplex

	ncr := hw.r(NCR)
	hw.w(NCR, ncr&^uint32(ncrTE|ncrRE))
	hw.applyLink()
	hw.w(NCR, ncr)
	hw.DMA.Barrier()
}

// setMAC programs the station address into SA1B/SA1T. The SA1T write activates
// the entry, so it is written last.
func (hw *GEM) setMAC() {
	m := hw.MAC
	hw.w(SA1B, uint32(m[0])|uint32(m[1])<<8|uint32(m[2])<<16|uint32(m[3])<<24)
	hw.w(SA1T, uint32(m[4])|uint32(m[5])<<8)
}

// SetMAC changes the station address after Init.
func (hw *GEM) SetMAC(mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return errors.New("gem: invalid MAC length")
	}
	hw.Lock()
	defer hw.Unlock()
	hw.MAC = mac
	hw.setMAC()
	return nil
}

// Start allocates the transmit and receive rings and enables the data path.
// Rings from a previous Start are released first.
func (hw *GEM) Start() error {
	hw.Lock()
	defer hw.Unlock()

	if hw.started {
		return errors.New("gem: already started")
	}
	if hw.Reg == nil || hw.DMA == nil {
		return errors.New("gem: not initialized")
	}

	hw.tx.init(hw.DMA, hw.RingSize, false)
	hw.rx.init(hw.DMA, hw.RingSize, true)

	// Queue pointers are only writable with TX/RX halted, the low half is
	// written last as it latches the pointer.
	hw.w(NCR, hw.r(NCR)&^uint32(ncrTE|ncrRE))

	tbus := hw.DMA.Bus(uint64(hw.tx.descAddr))
	hw.w(TBQPH, uint32(tbus>>32))
	hw.w(TBQP, uint32(tbus))

	rbus := hw.DMA.Bus(uint64(hw.rx.descAddr))
	hw.w(RBQPH, uint32(rbus>>32))
	hw.w(RBQP, uint32(rbus))

	hw.DMA.Barrier()
	hw.w(NCR, hw.r(NCR)|ncrTE|ncrRE)
	hw.DMA.Barrier()

	hw.started = true
	return nil
}

// Stop disables the data path. The rings remain allocated.
func (hw *GEM) Stop() {
	hw.Lock()
	defer hw.Unlock()
	hw.w(NCR, hw.r(NCR)&^uint32(ncrTE|ncrRE))
	hw.DMA.Barrier()
	hw.started = false
}

// EnableInterrupts enables TX/RX completion and error interrupts.
func (hw *GEM) EnableInterrupts() {
	hw.w(IER, intDefault)
}

// DisableInterrupts masks all interrupt causes.
func (hw *GEM) DisableInterrupts() {
	hw.w(IDR, 0xffffffff)
}

// HandleIRQ acknowledges asserted interrupts and invokes the TX/RX completion
// handlers, it returns the ISR value. Used bit read (queue empty) is not
// treated as an error.
func (hw *GEM) HandleIRQ() uint32 {
	status := hw.r(ISR)
	if status == 0 {
		return 0
	}
	hw.w(ISR, status) // W1C the asserted causes

	if status&intTCOMP != 0 {
		hw.w(TSR, tsrCOMP|tsrUBR) // clear completion + benign queue-empty
		if hw.TXHandler != nil {
			hw.TXHandler()
		}
	}
	if status&intRCOMP != 0 {
		hw.w(RSR, rsrREC|rsrBNA)
		if hw.RXHandler != nil {
			hw.RXHandler()
		}
	}
	if status&(intTXERR|intTUND|intRLE) != 0 {
		hw.w(TSR, tsrErrors)
	}
	if status&intROVR != 0 {
		hw.w(RSR, rsrOVR)
	}
	if status&intHRESP != 0 {
		hw.w(TSR, tsrHRESP)
		hw.w(RSR, rsrHRESP)
	}

	return status
}

// InterruptStatus returns the raw ISR value for diagnostics.
func (hw *GEM) InterruptStatus() uint32 { return hw.r(ISR) }

// Stats returns the transmitted, broadcast transmitted and received frame
// counters.
func (hw *GEM) Stats() (txOK, txBroadcast, rxOK uint32) {
	return hw.r(TXCNT), hw.r(TXBCNT), hw.r(RXCNT)
}

// manFrame builds a clause-22 MDIO frame for the MAN register.
func manFrame(op, pa, ra uint32, data uint16) uint32 {
	return manSOF | op | (pa&0x1f)<<23 | (ra&0x1f)<<18 | manCode | uint32(data)
}

// mdioIdle polls NSR.IDLE, returning false on timeout (dead MDC, absent PHY).
func (hw *GEM) mdioIdle() bool {
	for i := 0; i < mdioPollCount; i++ {
		if hw.r(NSR)&nsrIDLE != 0 {
			return true
		}
		time.Sleep(mdioPollInterval)
	}
	return false
}

// ReadPHY performs a clause-22 MDIO read, ok is false on timeout. It does not
// hold the data path lock, so PHY polling never stalls Transmit/Receive.
func (hw *GEM) ReadPHY(pa, ra int) (data uint16, ok bool) {
	hw.mdioMu.Lock()
	defer hw.mdioMu.Unlock()

	hw.w(MAN, manFrame(manRead, uint32(pa), uint32(ra), 0))
	if !hw.mdioIdle() {
		return 0, false
	}
	return uint16(hw.r(MAN)), true
}

// WritePHY performs a clause-22 MDIO write, ok is false on timeout.
func (hw *GEM) WritePHY(pa, ra int, data uint16) (ok bool) {
	hw.mdioMu.Lock()
	defer hw.mdioMu.Unlock()

	hw.w(MAN, manFrame(manWrite, uint32(pa), uint32(ra), data))
	return hw.mdioIdle()
}
