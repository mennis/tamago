// Synopsys DesignWare SPI master driver (DW_apb_ssi / DWC_ssi)
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package spi implements a polled driver for the Synopsys DesignWare SPI
// master as instantiated in the Raspberry Pi 5 RP1 southbridge (spi@50000, DT
// "snps,dw-apb-ssi"), following the Linux dw_spi driver (spi-dw.h,
// spi-dw-core.c).
//
// Register access goes through the injected [RegIO] interface, so the driver
// logic can be tested on the host.
//
// The RP1 instance has the following characteristics:
//   - SSI_VERSION_ID reads 0x3430322a ("4.02*", a DWC_ssi version) but CTRLR0
//     has the DW_apb_ssi layout (see [Layout]), with the frame size in DFS_32
//     [20:16].
//   - SER must be non-zero for the controller to clock any data, even when
//     chip-select is a GPIO.
//   - The TX FIFO is 64 frames deep, Init measures it as TXFTLR can read
//     back 0.
//
// Chip-select is not driven by this package, see [SPI.Slave].
//
// This package is only meant to be used with `GOOS=tamago` as supported by the
// TamaGo framework for bare metal Go, see https://github.com/usbarmory/tamago.
package spi

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// SPI registers (spi-dw.h), common to both layouts
const (
	CTRLR0  = 0x00 // control 0: frame size, SPI mode, transfer mode
	CTRLR1  = 0x04 // control 1: NDF, the frame count for RX-only/EEPROM modes
	SSIENR  = 0x08 // SSI enable
	MWCR    = 0x0c // Microwire control
	SER     = 0x10 // slave (native chip-select) enable
	BAUDR   = 0x14 // baud rate select: SCKDV
	TXFTLR  = 0x18 // transmit FIFO threshold
	RXFTLR  = 0x1c // receive FIFO threshold
	TXFLR   = 0x20 // transmit FIFO level (frames queued)
	RXFLR   = 0x24 // receive FIFO level (frames waiting)
	SR      = 0x28 // status
	IMR     = 0x2c // interrupt mask
	ISR     = 0x30 // interrupt status (masked)
	RISR    = 0x34 // raw interrupt status
	TXOICR  = 0x38 // TX overflow interrupt clear (read to clear)
	RXOICR  = 0x3c // RX overflow interrupt clear (read to clear)
	RXUICR  = 0x40 // RX underflow interrupt clear (read to clear)
	MSTICR  = 0x44 // multi-master contention interrupt clear (read to clear)
	ICR     = 0x48 // interrupt clear (read to clear)
	DMACR   = 0x4c // DMA control
	DMATDLR = 0x50 // DMA transmit data level
	DMARDLR = 0x54 // DMA receive data level
	IDR     = 0x58 // identification
	VERSION = 0x5c // SSI component version (SSI_VERSION_ID)
	DR      = 0x60 // data register: the FIFO window
)

// Layout represents the CTRLR0 field arrangement, which cannot be derived from
// the version register (Linux takes it from the device tree compatible string).
type Layout int

const (
	// APB is the DW_apb_ssi layout, used by RP1: FRF [5:4], SCPH [6],
	// SCPOL [7], TMOD [9:8], SLV_OE [10], SRL [11], CFS [15:12], and the
	// frame size in DFS [3:0] or DFS_32 [20:16].
	APB Layout = iota

	// DWC is the DWC_ssi layout: DFS [4:0], FRF [7:6], SCPH [8], SCPOL [9],
	// TMOD [11:10], SRL [13].
	DWC
)

// fields represents the CTRLR0 bit positions of a layout.
type fields struct {
	scph      uint32
	scpol     uint32
	srl       uint32
	tmodShift uint
	dfsShift  uint
	dfsMask   uint32
}

func (l Layout) fields() fields {
	if l == DWC {
		return fields{
			scph:      1 << 8,
			scpol:     1 << 9,
			srl:       1 << 13,
			tmodShift: 10,
			dfsShift:  0,
			dfsMask:   0x1f,
		}
	}

	return fields{
		scph:      1 << 6,
		scpol:     1 << 7,
		srl:       1 << 11,
		tmodShift: 8,
		dfsShift:  0, // replaced by dfs32Shift when Init detects DFS_32
		dfsMask:   0x0f,
	}
}

// APB layout DFS_32 field, used when DFS [3:0] is unimplemented (see
// resolveDFS).
const (
	dfs32Shift = 16
	dfs32Mask  = 0x1f
	dfsLowMask = 0x0f
)

// transfer mode field width, in both layouts
const tmodMask = 0x3

// SR (status register) bits.
const (
	srBUSY = 1 << 0 // a serial transfer is in progress
	srTFNF = 1 << 1 // TX FIFO not full
	srTFE  = 1 << 2 // TX FIFO empty
	srRFNE = 1 << 3 // RX FIFO not empty
	srRFF  = 1 << 4 // RX FIFO full
	srTXE  = 1 << 5 // transmission error (slave mode only)
	srDCOL = 1 << 6 // data collision (multi-master)
)

// RISR (raw interrupt status) bits, masked in IMR but polled to report FIFO
// overflow and underflow.
const (
	risrTXEI = 1 << 0 // TX FIFO empty
	risrTXOI = 1 << 1 // TX FIFO overflow
	risrRXUI = 1 << 2 // RX FIFO underflow
	risrRXOI = 1 << 3 // RX FIFO overflow
	risrRXFI = 1 << 4 // RX FIFO full
)

// TX FIFO threshold field, only log2(FIFO depth) bits are implemented
const txftlrTFTMask = 0xffff

// frame size, so that one byte is one frame
const frameBits = 8

// maximum FIFO depth allowed by the IP
const maxFIFO = 256

// defaultTimeout bounds the time a transfer may make no progress, it exceeds
// one FIFO load at the slowest SCLK (64 frames at 200 MHz/65534 take 168 ms).
const defaultTimeout = 1 * time.Second

// RegIO represents 32-bit MMIO access at absolute addresses.
type RegIO interface {
	Read32(addr uint64) uint32
	Write32(addr uint64, val uint32)
}

// TransferMode represents the CTRLR0.TMOD transfer mode.
type TransferMode uint32

const (
	// TXRX is full duplex, every transmitted frame produces a received frame
	// which must be drained.
	TXRX TransferMode = 0
	// TXOnly transmits only, leaving the RX FIFO empty.
	TXOnly TransferMode = 1
	// RXOnly receives only, not supported as CTRLR1.NDF is not programmed.
	RXOnly TransferMode = 2
	// EEPROM is the read after write mode, not supported.
	EEPROM TransferMode = 3
)

// SPI represents a DesignWare SPI master controller instance.
//
// Base, Reg, Clock and Speed must be set before Init, other fields have usable
// zero values.
type SPI struct {
	// mu serializes Write calls, a chip-select framed message spanning several
	// writes must be serialized by the caller.
	mu sync.Mutex

	// Base register
	Base uint64
	// Register access
	Reg RegIO

	// Layout is the CTRLR0 layout, the zero value (APB) is correct for RP1.
	Layout Layout

	// Clock is the reference clock (ssi_clk) in Hz, on RP1 this is clk_sys.
	Clock int

	// Speed is the requested SCLK in Hz, the divisor is rounded up so that
	// the actual rate (see [SPI.SCLK]) never exceeds it.
	Speed int

	// Mode is the SPI mode 0..3 (CPOL = Mode>>1, CPHA = Mode&1).
	Mode int

	// Slave is the native chip-select index written to SER.
	//
	// The native chip-select is framed per transfer, devices which need it
	// framed per message should use a GPIO chip-select driven by the caller.
	// SER is always non-zero as the controller does not clock otherwise, the
	// native output is harmless if not muxed onto a pin.
	Slave int

	// TransferMode is CTRLR0.TMOD (default full duplex).
	TransferMode TransferMode

	// FIFO is the TX FIFO depth in frames, when zero Init measures it.
	FIFO int

	// Timeout bounds the time a transfer may make no progress (default 1s).
	Timeout time.Duration

	f         fields // resolved CTRLR0 field positions
	div       uint32 // BAUDR (SCKDV), non-zero once initialized
	version   uint32 // SSI_VERSION_ID
	id        uint32 // IDR
	threshold uint32 // TXFTLR threshold field readback
}

func (hw *SPI) rd(off uint64) uint32      { return hw.Reg.Read32(hw.Base + off) }
func (hw *SPI) wr(off uint64, val uint32) { hw.Reg.Write32(hw.Base+off, val) }

// Init initializes and enables the controller, it must be called again to
// change Speed or Mode.
//
// Control registers are only writable with SSIENR cleared (writes are
// otherwise silently dropped), clearing it also empties both FIFOs.
//
// The configuration is read back to detect a wrong Layout, which only works
// when the misplaced bits are unimplemented (e.g. DWC on RP1).
func (hw *SPI) Init() error {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	switch {
	case hw.Reg == nil:
		return errors.New("spi: RegIO required")
	case hw.Base == 0:
		return errors.New("spi: controller base address required")
	case hw.Clock <= 0:
		return errors.New("spi: reference clock (Clock) required")
	case hw.Speed <= 0:
		return errors.New("spi: target clock (Speed) required")
	case hw.Mode < 0 || hw.Mode > 3:
		return fmt.Errorf("spi: invalid SPI mode %d", hw.Mode)
	case hw.Slave < 0 || hw.Slave > 31:
		return fmt.Errorf("spi: invalid chip-select index %d", hw.Slave)
	case hw.Layout != APB && hw.Layout != DWC:
		return fmt.Errorf("spi: invalid CTRLR0 layout %d", hw.Layout)
	case hw.TransferMode != TXRX && hw.TransferMode != TXOnly:
		return fmt.Errorf("spi: transfer mode %d not supported (RX length comes from CTRLR1.NDF, which this driver does not program)", hw.TransferMode)
	}

	hw.div = 0
	hw.f = hw.Layout.fields()

	hw.wr(SSIENR, 0)

	hw.version = hw.rd(VERSION)
	hw.id = hw.rd(IDR)

	// detect a register window which is not decoding
	if hw.version == 0 || hw.version == 0xffffffff {
		return fmt.Errorf("spi: SSI_VERSION_ID reads %#08x — the register window is not decoding", hw.version)
	}

	hw.resolveDFS()
	hw.threshold = hw.probeThreshold()

	div := divisor(hw.Clock, hw.Speed)

	// with no slave selected the controller does not drain the FIFO
	if hw.FIFO == 0 {
		hw.program(hw.TransferMode, div, 0, 0)

		depth := hw.measureFIFO()
		if depth == 0 || depth > maxFIFO {
			return fmt.Errorf("spi: TX FIFO never filled (SR %#x after %d frames); the controller is not accepting data", hw.rd(SR), maxFIFO)
		}

		hw.FIFO = depth
	}

	hw.program(hw.TransferMode, div, 0, 1<<uint(hw.Slave))

	if err := hw.verify(hw.TransferMode, div); err != nil {
		return err
	}

	hw.div = div

	return nil
}

// resolveDFS detects whether the APB layout frame size is in DFS [3:0] or
// DFS_32 [20:16], as in Linux dw_spi_hw_init, by writing all-ones to CTRLR0
// (RP1 reads back 0x011fffc0, DFS_32).
func (hw *SPI) resolveDFS() {
	if hw.Layout == DWC {
		return
	}

	saved := hw.rd(CTRLR0)

	hw.wr(CTRLR0, 0xffffffff)
	probe := hw.rd(CTRLR0)

	hw.wr(CTRLR0, saved)

	if probe&dfsLowMask == 0 {
		hw.f.dfsShift = dfs32Shift
		hw.f.dfsMask = dfs32Mask
	}
}

// probeThreshold returns the implemented TX FIFO threshold field bits. It is
// only reported (see [SPI.ThresholdMask]), as it can read back 0 on RP1.
func (hw *SPI) probeThreshold() uint32 {
	saved := hw.rd(TXFTLR)

	hw.wr(TXFTLR, txftlrTFTMask)
	mask := hw.rd(TXFTLR) & txftlrTFTMask

	hw.wr(TXFTLR, saved)

	return mask
}

// measureFIFO returns the TX FIFO depth, by filling it until SR.TFNF clears,
// and then flushes it. The controller must be enabled with SER cleared, so
// that no transfer starts.
func (hw *SPI) measureFIFO() int {
	n := 0

	// inclusive, to measure a maxFIFO deep FIFO
	for ; n <= maxFIFO; n++ {
		if hw.rd(SR)&srTFNF == 0 {
			break
		}

		hw.wr(DR, 0)
	}

	hw.wr(SSIENR, 0)

	// full before any write, or never full
	if n == 0 || n > maxFIFO {
		return 0
	}

	return n
}

// program writes the transfer configuration, with extra OR'd into CTRLR0 and
// ser written to SER, and enables the controller.
func (hw *SPI) program(tmod TransferMode, div uint32, extra uint32, ser uint32) {
	hw.wr(SSIENR, 0)

	// interrupts are not used, clear any previous owner configuration
	hw.wr(TXFTLR, 0)
	hw.wr(RXFTLR, 0)
	hw.wr(IMR, 0)
	hw.wr(BAUDR, div)

	// DFS_32 ignores a write of 0 (one-bit frames are not supported)
	ctrl := uint32(frameBits-1) << hw.f.dfsShift
	ctrl |= uint32(tmod) << hw.f.tmodShift
	ctrl |= extra

	if hw.Mode&1 != 0 {
		ctrl |= hw.f.scph
	}
	if hw.Mode&2 != 0 {
		ctrl |= hw.f.scpol
	}
	hw.wr(CTRLR0, ctrl)

	hw.wr(SER, ser)
	hw.wr(SSIENR, 1)

	// clear any stale interrupt (read to clear)
	hw.rd(ICR)
}

// verify reads back the configuration, as a misplaced field otherwise fails
// silently.
func (hw *SPI) verify(tmod TransferMode, div uint32) error {
	ctrl := hw.rd(CTRLR0)

	if got := (ctrl >> hw.f.dfsShift) & hw.f.dfsMask; got != frameBits-1 {
		return fmt.Errorf("spi: CTRLR0 %#08x has frame size %d at bit %d, want %d — wrong register layout for this controller", ctrl, got+1, hw.f.dfsShift, frameBits)
	}

	if got := (ctrl >> hw.f.tmodShift) & tmodMask; got != uint32(tmod) {
		return fmt.Errorf("spi: CTRLR0 %#08x has transfer mode %d at bit %d, want %d — wrong register layout for this controller", ctrl, got, hw.f.tmodShift, tmod)
	}

	if got := hw.rd(BAUDR) & 0xffff; got != div {
		return fmt.Errorf("spi: BAUDR reads %d, want %d — the controller did not accept the clock divisor", got, div)
	}

	if hw.rd(SER) == 0 {
		return errors.New("spi: SER reads 0 — the controller will accept data and never clock it out")
	}

	return nil
}

// divisor computes BAUDR (SCKDV) for a target SCLK, rounding up to an even
// divisor as SCKDV bit 0 is hardwired to zero and 0 stops the clock.
func divisor(clock, speed int) uint32 {
	div := (clock + speed - 1) / speed
	div = (div + 1) &^ 1

	switch {
	case div < 2:
		div = 2
	case div > 0xfffe:
		div = 0xfffe
	}

	return uint32(div)
}

// SCLK returns the actual serial clock in Hz, or 0 before Init.
func (hw *SPI) SCLK() int {
	if hw.div == 0 {
		return 0
	}

	return hw.Clock / int(hw.div)
}

// Version returns SSI_VERSION_ID, four big-endian ASCII characters (e.g.
// 0x3430322a is "4.02*").
func (hw *SPI) Version() uint32 { return hw.version }

// ID returns the IDR register, which reads all-ones on RP1.
func (hw *SPI) ID() uint32 { return hw.id }

// ThresholdMask returns the TX FIFO threshold field readback after writing
// all-ones, which is either 0 or one less than the FIFO depth.
func (hw *SPI) ThresholdMask() uint32 { return hw.threshold }

// FrameSizeShift returns the CTRLR0 frame size bit position, 0 for DFS or 16
// for DFS_32.
func (hw *SPI) FrameSizeShift() uint { return hw.f.dfsShift }

// Status returns the raw SR (status) register.
func (hw *SPI) Status() uint32 { return hw.rd(SR) }

// Write transmits buf, one 8-bit frame per byte MSB first, and returns once
// the last bit has left the shift register so that the caller can then
// release chip-select, which is not driven by Write.
func (hw *SPI) Write(buf []byte) error {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.div == 0 {
		return errors.New("spi: controller not initialized")
	}

	if len(buf) == 0 {
		return nil
	}

	// In full duplex each TX frame produces an RX frame, transmission must
	// stay within a FIFO depth of the discarded reads to avoid RX overflow.
	duplex := hw.TransferMode == TXRX

	timeout := hw.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	sent, recv := 0, 0
	deadline := time.Now().Add(timeout)

	for sent < len(buf) {
		if duplex {
			recv += hw.drain(sent - recv)
		}

		room := hw.FIFO - int(hw.rd(TXFLR))
		if duplex {
			if gap := hw.FIFO - (sent - recv); gap < room {
				room = gap
			}
		}

		if room <= 0 {
			if time.Now().After(deadline) {
				return fmt.Errorf("spi: stalled after %d of %d bytes (SR %#x, RISR %#x)", sent, len(buf), hw.rd(SR), hw.rd(RISR))
			}
			continue
		}

		if n := len(buf) - sent; n < room {
			room = n
		}

		for _, b := range buf[sent : sent+room] {
			hw.wr(DR, uint32(b))
		}

		sent += room
		deadline = time.Now().Add(timeout)
	}

	if err := hw.waitIdle(timeout); err != nil {
		return err
	}

	if duplex {
		// leave the RX FIFO empty for the next Write
		hw.drain(hw.FIFO)
	}

	if risr := hw.rd(RISR); risr&(risrTXOI|risrRXOI|risrRXUI) != 0 {
		hw.rd(ICR)
		return fmt.Errorf("spi: FIFO error after %d bytes (RISR %#x)", sent, risr)
	}

	return nil
}

// drain discards up to max frames from the RX FIFO, returning their number.
func (hw *SPI) drain(max int) (n int) {
	for n < max && hw.rd(SR)&srRFNE != 0 {
		hw.rd(DR)
		n++
	}

	return
}

// waitIdle waits for both TFE and !BUSY, as TFE alone leaves one frame in
// flight and BUSY is briefly clear between frames.
func (hw *SPI) waitIdle(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		if sr := hw.rd(SR); sr&srTFE != 0 && sr&srBUSY == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("spi: controller still busy after %s (SR %#x)", timeout, hw.rd(SR))
		}
	}
}

// LoopbackTest transfers buf with the CTRLR0 shift register loopback (SRL)
// enabled and returns the received frames. buf must fit in the FIFO.
//
// The loopback is internal to the controller and does not test the pins. The
// previous configuration is restored on return.
func (hw *SPI) LoopbackTest(buf []byte) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.div == 0 {
		return nil, errors.New("spi: controller not initialized")
	}

	if len(buf) == 0 || len(buf) > hw.FIFO {
		return nil, fmt.Errorf("spi: loopback length %d outside 1..%d (one FIFO load, since nothing drains mid-flight)", len(buf), hw.FIFO)
	}

	timeout := hw.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	defer hw.program(hw.TransferMode, hw.div, 0, 1<<uint(hw.Slave))

	hw.program(TXRX, hw.div, hw.f.srl, 1<<uint(hw.Slave))

	for _, b := range buf {
		hw.wr(DR, uint32(b))
	}

	if err := hw.waitIdle(timeout); err != nil {
		return nil, err
	}

	got := make([]byte, 0, len(buf))
	deadline := time.Now().Add(timeout)

	for len(got) < len(buf) {
		if hw.rd(SR)&srRFNE == 0 {
			if time.Now().After(deadline) {
				return got, fmt.Errorf("spi: loopback returned %d of %d frames (SR %#x, RISR %#x)", len(got), len(buf), hw.rd(SR), hw.rd(RISR))
			}
			continue
		}

		got = append(got, byte(hw.rd(DR)))
	}

	return got, nil
}

// Disable disables the controller, Init must be called again before any
// transfer.
func (hw *SPI) Disable() {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	hw.wr(SSIENR, 0)
	hw.div = 0
}
