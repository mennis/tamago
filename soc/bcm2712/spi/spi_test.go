// Synopsys DesignWare SPI master driver (DW_apb_ssi / DWC_ssi)
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package spi

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// model represents the CTRLR0 layout presented by the fake.
type model int

const (
	// DW_apb_ssi with DFS_32 [20:16], [3:0] unimplemented and a write of 0
	// ignored (RP1)
	modelAPBDFS32 model = iota
	// DW_apb_ssi with DFS [3:0]
	modelAPBLow
	// DWC_ssi: DFS [4:0], TMOD [11:10], SRL [13]
	modelDWC
)

func (m model) tmodShift() uint {
	if m == modelDWC {
		return 10
	}

	return 8
}

func (m model) srlBit() uint32 {
	if m == modelDWC {
		return 1 << 13
	}

	return 1 << 11
}

// fakeSPI models a DesignWare SPI controller: control registers are writable
// only while disabled, CTRLR0 implements only its layout bits, nothing shifts
// with SER cleared and the FIFOs are finite.
type fakeSPI struct {
	mu   sync.Mutex
	base uint64

	layout  model
	depth   int
	version uint32
	id      uint32

	// frames shifted out per SR/TXFLR read, 0 stalls the transfer
	shiftPerPoll int

	enabled bool
	ctrlr0  uint32
	baudr   uint32
	ser     uint32
	imr     uint32
	txftlr  uint32
	rxftlr  uint32
	risr    uint32

	tx  []byte // frames queued in the TX FIFO
	rx  []byte // frames waiting in the RX FIFO
	out []byte // frames shifted onto the wire, in order

	// TXFTLR reads back 0
	txftlrDead bool

	// shift register loopback does not work
	ignoreSRL bool

	// control register writes dropped as the controller was enabled
	writesWhileEnabled int
}

func newFake(base uint64, depth int, m model) *fakeSPI {
	return &fakeSPI{
		base:         base,
		layout:       m,
		depth:        depth,
		version:      0x3430322a, // RP1 "4.02*"
		id:           0xffffffff, // RP1 does not populate IDR
		shiftPerPoll: depth,
		ctrlr0:       0x01070000, // as left by Linux on RP1
	}
}

// writeCTRLR0 keeps only the bits the modelled layout implements.
func (f *fakeSPI) writeCTRLR0(v uint32) {
	switch f.layout {
	case modelDWC:
		f.ctrlr0 = v & 0x3fff
	case modelAPBLow:
		f.ctrlr0 = v & 0xffff
	default: // modelAPBDFS32
		next := v & 0x001ffff0 // [3:0] unimplemented, up to DFS_32 at [20:16]
		if v&(dfs32Mask<<dfs32Shift) == 0 {
			next |= f.ctrlr0 & (dfs32Mask << dfs32Shift)
		}
		f.ctrlr0 = next
	}
}

// shift moves up to shiftPerPoll frames from the TX FIFO onto the wire, in
// full duplex producing one RX frame for each, on any status read.
func (f *fakeSPI) shift() {
	if !f.enabled || f.ser == 0 {
		return
	}

	n := f.shiftPerPoll
	if n > len(f.tx) {
		n = len(f.tx)
	}

	shifted := f.tx[:n]
	f.out = append(f.out, shifted...)
	f.tx = f.tx[n:]

	if (f.ctrlr0>>f.layout.tmodShift())&tmodMask != uint32(TXRX) {
		return
	}

	// received frames are zero unless in loopback
	for _, b := range shifted {
		if len(f.rx) >= f.depth {
			f.risr |= risrRXOI
			continue
		}
		if f.ctrlr0&f.layout.srlBit() == 0 || f.ignoreSRL {
			b = 0
		}
		f.rx = append(f.rx, b)
	}
}

func (f *fakeSPI) status() uint32 {
	sr := uint32(0)

	if len(f.tx) == 0 {
		sr |= srTFE
	}
	if len(f.tx) < f.depth {
		sr |= srTFNF
	}
	if len(f.tx) > 0 {
		sr |= srBUSY
	}
	if len(f.rx) > 0 {
		sr |= srRFNE
	}
	if len(f.rx) >= f.depth {
		sr |= srRFF
	}

	return sr
}

func (f *fakeSPI) Read32(addr uint64) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch addr - f.base {
	case VERSION:
		return f.version
	case IDR:
		return f.id
	case SSIENR:
		if f.enabled {
			return 1
		}
		return 0
	case CTRLR0:
		return f.ctrlr0
	case BAUDR:
		return f.baudr
	case SER:
		return f.ser
	case IMR:
		return f.imr
	case TXFTLR:
		return f.txftlr
	case RXFTLR:
		return f.rxftlr
	case TXFLR:
		f.shift()
		return uint32(len(f.tx))
	case RXFLR:
		f.shift()
		return uint32(len(f.rx))
	case SR:
		f.shift()
		return f.status()
	case RISR:
		return f.risr
	case ICR:
		f.risr = 0
		return 0
	case DR:
		if len(f.rx) == 0 {
			f.risr |= risrRXUI
			return 0
		}
		b := f.rx[0]
		f.rx = f.rx[1:]
		return uint32(b)
	}

	return 0
}

func (f *fakeSPI) Write32(addr uint64, val uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()

	off := addr - f.base

	if off == SSIENR {
		f.enabled = val&1 != 0
		if !f.enabled {
			// clearing SSI_EN resets both FIFOs
			f.tx, f.rx = nil, nil
		}
		return
	}

	if off == DR {
		if !f.enabled {
			return
		}
		if len(f.tx) >= f.depth {
			f.risr |= risrTXOI
			return
		}
		f.tx = append(f.tx, byte(val))
		return
	}

	// control registers are writable only while disabled
	if f.enabled {
		f.writesWhileEnabled++
		return
	}

	switch off {
	case CTRLR0:
		f.writeCTRLR0(val)
	case BAUDR:
		f.baudr = val &^ 1 // SCKDV bit 0 is hardwired to zero
	case SER:
		f.ser = val
	case IMR:
		f.imr = val
	case TXFTLR:
		if f.txftlrDead {
			return
		}
		f.txftlr = val & uint32(f.depth-1)
	case RXFTLR:
		f.rxftlr = val & uint32(f.depth-1)
	}
}

const testBase = 0x1f00050000

// newSPI returns a driver attached to a model of the RP1 controller.
func newSPI(depth int) (*SPI, *fakeSPI) { return newSPIModel(depth, modelAPBDFS32) }

func newSPIModel(depth int, m model) (*SPI, *fakeSPI) {
	f := newFake(testBase, depth, m)

	hw := &SPI{
		Base:    testBase,
		Reg:     f,
		Clock:   200_000_000,
		Speed:   16_000_000,
		Timeout: 100 * time.Millisecond,
	}

	if m == modelDWC {
		hw.Layout = DWC
	}

	return hw, f
}

func TestInitProgramsController(t *testing.T) {
	hw, f := newSPI(64)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if f.writesWhileEnabled != 0 {
		t.Errorf("%d control-register writes made against an enabled controller; hardware drops those", f.writesWhileEnabled)
	}

	// RP1: frame size in DFS_32 [20:16], transfer mode in [9:8]
	if got := (f.ctrlr0 >> dfs32Shift) & dfs32Mask; got != frameBits-1 {
		t.Errorf("CTRLR0 %#08x frame size field = %d, want %d at bit %d", f.ctrlr0, got, frameBits-1, dfs32Shift)
	}
	if got := (f.ctrlr0 >> 8) & tmodMask; got != uint32(TXOnly) {
		t.Errorf("CTRLR0 %#08x transfer mode = %d at bit 8, want %d", f.ctrlr0, got, TXOnly)
	}
	if f.ctrlr0&dfsLowMask != 0 {
		t.Errorf("CTRLR0 %#08x has bits set in DFS [3:0], which this part does not implement", f.ctrlr0)
	}
	if got := hw.FrameSizeShift(); got != dfs32Shift {
		t.Errorf("FrameSizeShift = %d, want %d", got, dfs32Shift)
	}

	// 200 MHz / 16 MHz = 12.5, rounded up to an even divisor
	if f.baudr != 14 {
		t.Errorf("BAUDR = %d, want 14", f.baudr)
	}
	if got, want := hw.SCLK(), 200_000_000/14; got != want {
		t.Errorf("SCLK = %d, want %d", got, want)
	}

	// nothing clocks with SER cleared
	if f.ser == 0 {
		t.Error("SER = 0; the controller will accept DR writes and never toggle SCLK")
	}
	if f.imr != 0 {
		t.Errorf("IMR = %#x, want 0 (polled driver)", f.imr)
	}
	if !f.enabled {
		t.Error("controller left disabled")
	}
	if hw.FIFO != 64 {
		t.Errorf("FIFO = %d, want 64", hw.FIFO)
	}
	if len(f.tx) != 0 {
		t.Errorf("Init left %d frames in the TX FIFO from the measurement", len(f.tx))
	}
	if hw.Version() != 0x3430322a || hw.ID() != 0xffffffff {
		t.Errorf("identity = %#08x/%#08x, want 0x3430322a/0xffffffff", hw.Version(), hw.ID())
	}
}

// TestInitResolvesFrameSizeField checks frame size field detection.
func TestInitResolvesFrameSizeField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		m     model
		shift uint
		mask  uint32
	}{
		{"DW_apb_ssi with DFS_32", modelAPBDFS32, dfs32Shift, dfs32Mask},
		{"DW_apb_ssi with plain DFS", modelAPBLow, 0, dfsLowMask},
		{"DWC_ssi", modelDWC, 0, 0x1f},
	} {
		hw, f := newSPIModel(64, tc.m)

		if err := hw.Init(); err != nil {
			t.Fatalf("%s: Init: %v", tc.name, err)
		}

		if got := hw.FrameSizeShift(); got != tc.shift {
			t.Errorf("%s: FrameSizeShift = %d, want %d", tc.name, got, tc.shift)
		}
		if got := (f.ctrlr0 >> tc.shift) & tc.mask; got != frameBits-1 {
			t.Errorf("%s: CTRLR0 %#08x frame size = %d, want %d", tc.name, f.ctrlr0, got, frameBits-1)
		}
	}
}

// TestInitRejectsWrongLayout checks that Init detects the DWC layout on RP1,
// whose unimplemented CTRLR0 [3:0] reads back zero.
func TestInitRejectsWrongLayout(t *testing.T) {
	hw, _ := newSPIModel(64, modelAPBDFS32)
	hw.Layout = DWC
	hw.TransferMode = TXOnly
	hw.FIFO = 64

	err := hw.Init()
	if err == nil {
		t.Fatal("Init succeeded with the DWC layout against a DW_apb_ssi part")
	}
	if !strings.Contains(err.Error(), "layout") {
		t.Errorf("error does not name the layout: %v", err)
	}
}

// TestReadbackCannotCatchEveryLayoutMismatch documents that the readback does
// not detect a wrong layout whose misplaced bits are writable.
func TestReadbackCannotCatchEveryLayoutMismatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       model
		l       Layout
		tmodBit uint
	}{
		{"DW_apb_ssi driver, DWC part", modelDWC, APB, 10},
		{"DWC driver, plain-DFS DW_apb_ssi part", modelAPBLow, DWC, 8},
	} {
		hw, f := newSPIModel(64, tc.m)
		hw.Layout = tc.l
		hw.TransferMode = TXOnly
		hw.FIFO = 64

		if err := hw.Init(); err != nil {
			t.Skipf("%s: readback now catches this; narrow this test and update the Init comment: %v", tc.name, err)
		}

		// accepted, but still in full duplex
		if got := (f.ctrlr0 >> tc.tmodBit) & tmodMask; got == uint32(TXOnly) {
			t.Errorf("%s: the part ended up transmit-only, so this was not a mis-programming after all", tc.name)
		}
	}
}

func TestInitSPIMode(t *testing.T) {
	for mode, want := range map[int]uint32{
		0: 0,
		1: 1 << 6,
		2: 1 << 7,
		3: 1<<6 | 1<<7,
	} {
		hw, f := newSPI(64)
		hw.Mode = mode

		if err := hw.Init(); err != nil {
			t.Fatalf("mode %d: Init: %v", mode, err)
		}

		if got := f.ctrlr0 & (1<<6 | 1<<7); got != want {
			t.Errorf("mode %d: CTRLR0 clock bits = %#x, want %#x", mode, got, want)
		}
	}
}

// TestInitMeasuresFIFO checks the FIFO depth measurement, including with
// TXFTLR reading back 0.
func TestInitMeasuresFIFO(t *testing.T) {
	for _, depth := range []int{2, 4, 8, 16, 32, 64, 128, 256} {
		for _, dead := range []bool{false, true} {
			hw, f := newSPI(depth)
			f.txftlrDead = dead

			if err := hw.Init(); err != nil {
				t.Fatalf("depth %d (threshold dead=%v): Init: %v", depth, dead, err)
			}
			if hw.FIFO != depth {
				t.Errorf("threshold dead=%v: measured FIFO depth %d, want %d", dead, hw.FIFO, depth)
			}
		}
	}
}

// TestInitFIFOMeasurementDoesNotClock checks that the FIFO measurement is
// done with no slave selected, so that nothing reaches the wire.
func TestInitFIFOMeasurementDoesNotClock(t *testing.T) {
	hw, f := newSPI(64)
	f.shiftPerPoll = 1000

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if hw.FIFO != 64 {
		t.Errorf("measured FIFO depth %d, want 64 — the measurement let the controller clock", hw.FIFO)
	}
	if len(f.out) != 0 {
		t.Errorf("%d measurement bytes reached the wire; a real device would have seen them", len(f.out))
	}
}

func TestInitFIFOOverride(t *testing.T) {
	hw, f := newSPI(64)
	hw.FIFO = 32

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if hw.FIFO != 32 {
		t.Errorf("FIFO = %d, want the caller's 32", hw.FIFO)
	}
	if len(f.out) != 0 {
		t.Error("Init clocked bytes despite being told the FIFO depth")
	}
}

func TestInitDeadRegisterWindow(t *testing.T) {
	for _, v := range []uint32{0, 0xffffffff} {
		hw, f := newSPI(64)
		f.version = v

		err := hw.Init()
		if err == nil {
			t.Errorf("VERSION %#08x: Init succeeded against a window that is not decoding", v)
			continue
		}
		if !strings.Contains(err.Error(), "decoding") {
			t.Errorf("VERSION %#08x: error does not name the bus: %v", v, err)
		}
	}
}

func TestInitValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*SPI)
	}{
		{"no RegIO", func(s *SPI) { s.Reg = nil }},
		{"no base", func(s *SPI) { s.Base = 0 }},
		{"no reference clock", func(s *SPI) { s.Clock = 0 }},
		{"no target clock", func(s *SPI) { s.Speed = 0 }},
		{"bad SPI mode", func(s *SPI) { s.Mode = 4 }},
		{"bad chip-select", func(s *SPI) { s.Slave = 32 }},
		{"bad layout", func(s *SPI) { s.Layout = Layout(7) }},
		{"RX-only", func(s *SPI) { s.TransferMode = RXOnly }},
		{"EEPROM", func(s *SPI) { s.TransferMode = EEPROM }},
	} {
		hw, _ := newSPI(64)
		tc.mod(hw)

		if err := hw.Init(); err == nil {
			t.Errorf("%s: Init succeeded", tc.name)
		}
	}
}

func TestDivisor(t *testing.T) {
	for _, tc := range []struct {
		clock, speed int
		want         uint32
	}{
		// 12.5 rounds up to 14 (14.3 MHz), not down to 12 (16.7 MHz)
		{200_000_000, 16_000_000, 14},
		{200_000_000, 100_000_000, 2}, // fastest
		{200_000_000, 200_000_000, 2}, // clamped
		{200_000_000, 400_000_000, 2},
		{200_000_000, 50_000_000, 4},
		{200_000_000, 10_000_000, 20},
		{200_000_000, 1_000_000, 200},
		{200_000_000, 100_000, 2000},
		{200_000_000, 1, 0xfffe}, // clamped
		{48_000_000, 1_000_000, 48},
		{48_000_000, 7_000_000, 8}, // 6.86 -> 8 (6 MHz)
	} {
		if got := divisor(tc.clock, tc.speed); got != tc.want {
			t.Errorf("divisor(%d, %d) = %d, want %d", tc.clock, tc.speed, got, tc.want)
		}
	}
}

func TestDivisorNeverOverclocks(t *testing.T) {
	// the divisor is even and never exceeds the requested rate, except when
	// clamped to [clock/65534, clock/2]
	const clock = 200_000_000

	for speed := 1000; speed <= clock; speed += 7919 {
		div := divisor(clock, speed)

		if div&1 != 0 {
			t.Fatalf("divisor(%d, %d) = %d is odd; SCKDV bit 0 is hardwired to zero", clock, speed, div)
		}
		if div == 2 || div == 0xfffe {
			continue
		}
		if got := clock / int(div); got > speed {
			t.Fatalf("divisor(%d, %d) = %d yields %d Hz, above the requested rate", clock, speed, div, got)
		}
	}
}

// pattern returns n bytes, each distinguishable from its neighbors.
func pattern(n int) []byte {
	b := make([]byte, n)

	for i := range b {
		b[i] = byte(i*7 + i/251)
	}

	return b
}

func TestWriteTXOnly(t *testing.T) {
	hw, f := newSPI(64)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	buf := pattern(1000)
	if err := hw.Write(buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(f.out) != len(buf) {
		t.Fatalf("%d bytes reached the wire, want %d", len(f.out), len(buf))
	}
	for i := range buf {
		if f.out[i] != buf[i] {
			t.Fatalf("wire byte %d = %#x, want %#x", i, f.out[i], buf[i])
		}
	}
	if len(f.rx) != 0 {
		t.Errorf("RX FIFO holds %d frames in TX-only mode", len(f.rx))
	}
	if f.risr != 0 {
		t.Errorf("RISR = %#x, want no FIFO errors", f.risr)
	}
}

// TestWriteWaitsForShiftRegister checks that Write returns only once all
// bytes have been shifted out.
func TestWriteWaitsForShiftRegister(t *testing.T) {
	hw, f := newSPI(64)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	f.shiftPerPoll = 3

	buf := pattern(200)
	if err := hw.Write(buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(f.tx) != 0 {
		t.Errorf("Write returned with %d frames still in the TX FIFO", len(f.tx))
	}
	if len(f.out) != len(buf) {
		t.Errorf("%d bytes reached the wire, want %d", len(f.out), len(buf))
	}
	if f.status()&srBUSY != 0 {
		t.Error("Write returned while the controller was still BUSY")
	}
}

// TestWriteRespectsFIFODepth checks that Write never overflows the TX FIFO.
func TestWriteRespectsFIFODepth(t *testing.T) {
	for _, depth := range []int{4, 8, 64} {
		hw, f := newSPI(depth)
		hw.TransferMode = TXOnly

		if err := hw.Init(); err != nil {
			t.Fatalf("depth %d: Init: %v", depth, err)
		}

		f.shiftPerPoll = 1

		buf := pattern(depth * 5)
		if err := hw.Write(buf); err != nil {
			t.Fatalf("depth %d: Write: %v", depth, err)
		}

		if f.risr&risrTXOI != 0 {
			t.Errorf("depth %d: TX FIFO overflowed", depth)
		}
		if len(f.out) != len(buf) {
			t.Errorf("depth %d: %d bytes reached the wire, want %d", depth, len(f.out), len(buf))
		}
	}
}

// TestWriteDuplexDrainsRX checks that full duplex Write never overflows the
// RX FIFO and leaves it empty.
func TestWriteDuplexDrainsRX(t *testing.T) {
	for _, shift := range []int{1, 3, 64} {
		hw, f := newSPI(64)
		hw.TransferMode = TXRX

		if err := hw.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}

		f.shiftPerPoll = shift

		buf := pattern(500)
		if err := hw.Write(buf); err != nil {
			t.Fatalf("shift %d: Write: %v", shift, err)
		}

		if f.risr&risrRXOI != 0 {
			t.Errorf("shift %d: RX FIFO overflowed", shift)
		}
		if len(f.out) != len(buf) {
			t.Errorf("shift %d: %d bytes reached the wire, want %d", shift, len(f.out), len(buf))
		}
		if len(f.rx) != 0 {
			t.Errorf("shift %d: Write left %d frames in the RX FIFO for the next transfer to trip over", shift, len(f.rx))
		}
	}
}

// TestWriteFramesAcrossCalls checks that consecutive Writes concatenate on the
// wire in call order.
func TestWriteFramesAcrossCalls(t *testing.T) {
	hw, f := newSPI(64)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, part := range [][]byte{{0x61}, {0x02, 0x58, 0x01, 0xC0}, {0x10}} {
		if err := hw.Write(part); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	want := []byte{0x61, 0x02, 0x58, 0x01, 0xC0, 0x10}
	if string(f.out) != string(want) {
		t.Errorf("wire = % x, want % x", f.out, want)
	}
}

func TestWriteStall(t *testing.T) {
	hw, f := newSPI(8)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	f.shiftPerPoll = 0

	err := hw.Write(pattern(100))
	if err == nil {
		t.Fatal("Write succeeded against a controller that never drained")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("error does not name the stall: %v", err)
	}
	if !strings.Contains(err.Error(), "of 100 bytes") {
		t.Errorf("error does not report progress: %v", err)
	}
}

// TestWriteWithoutSER checks that Write fails with SER cleared.
func TestWriteWithoutSER(t *testing.T) {
	hw, f := newSPI(8)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	f.mu.Lock()
	f.ser = 0
	f.mu.Unlock()

	if err := hw.Write(pattern(100)); err == nil {
		t.Fatal("Write succeeded with SER clear")
	}
}

func TestWriteUninitialized(t *testing.T) {
	hw, _ := newSPI(64)

	if err := hw.Write([]byte{0x00}); err == nil {
		t.Fatal("Write succeeded before Init")
	}

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	hw.Disable()

	if err := hw.Write([]byte{0x00}); err == nil {
		t.Fatal("Write succeeded after Disable")
	}
}

func TestWriteEmpty(t *testing.T) {
	hw, f := newSPI(64)

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := hw.Write(nil); err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if len(f.out) != 0 {
		t.Errorf("%d bytes reached the wire for an empty Write", len(f.out))
	}
}

// TestWriteFrame checks a 600x448 4bpp frame transfer.
func TestWriteFrame(t *testing.T) {
	const frameBytes = 600 * 448 / 2

	hw, f := newSPI(64)
	hw.TransferMode = TXOnly

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	f.shiftPerPoll = 17

	buf := pattern(frameBytes)
	if err := hw.Write(buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(f.out) != frameBytes {
		t.Fatalf("%d bytes reached the wire, want %d", len(f.out), frameBytes)
	}
	for i := range buf {
		if f.out[i] != buf[i] {
			t.Fatalf("wire byte %d = %#x, want %#x", i, f.out[i], buf[i])
		}
	}
	if f.risr != 0 {
		t.Errorf("RISR = %#x, want no FIFO errors", f.risr)
	}
}

func TestSCLKBeforeInit(t *testing.T) {
	hw, _ := newSPI(64)

	if got := hw.SCLK(); got != 0 {
		t.Errorf("SCLK = %d before Init, want 0", got)
	}
}

// TestLoopback checks LoopbackTest on both layouts, and that the previous
// configuration is restored.
func TestLoopback(t *testing.T) {
	for _, m := range []model{modelAPBDFS32, modelDWC} {
		hw, f := newSPIModel(64, m)
		hw.TransferMode = TXOnly

		if err := hw.Init(); err != nil {
			t.Fatalf("layout %d: Init: %v", m, err)
		}

		before := f.ctrlr0
		buf := pattern(32)

		got, err := hw.LoopbackTest(buf)
		if err != nil {
			t.Fatalf("layout %d: LoopbackTest: %v", m, err)
		}
		if string(got) != string(buf) {
			t.Errorf("layout %d: loopback returned % x, want % x", m, got, buf)
		}

		if f.ctrlr0 != before {
			t.Errorf("layout %d: CTRLR0 left at %#x, want the pre-test %#x", m, f.ctrlr0, before)
		}
		if f.ctrlr0&m.srlBit() != 0 {
			t.Errorf("layout %d: controller left in shift-register loopback", m)
		}
		if !f.enabled {
			t.Errorf("layout %d: controller left disabled", m)
		}

		// transfers still work
		f.out = nil
		if err := hw.Write([]byte{0xAA, 0x55}); err != nil {
			t.Fatalf("layout %d: Write after LoopbackTest: %v", m, err)
		}
		if string(f.out) != string([]byte{0xAA, 0x55}) {
			t.Errorf("layout %d: post-test wire = % x, want aa 55", m, f.out)
		}
	}
}

// TestLoopbackWithoutSRL checks that a non-working loopback is reported.
func TestLoopbackWithoutSRL(t *testing.T) {
	hw, f := newSPI(64)

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	f.ignoreSRL = true

	got, err := hw.LoopbackTest([]byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatalf("LoopbackTest: %v", err)
	}
	if string(got) == string([]byte{0x01, 0x02, 0x03}) {
		t.Fatal("loopback reported the sent bytes from a controller that does not loop back")
	}
}

func TestLoopbackBounds(t *testing.T) {
	hw, _ := newSPI(64)

	if _, err := hw.LoopbackTest([]byte{0x00}); err == nil {
		t.Error("LoopbackTest succeeded before Init")
	}

	if err := hw.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, err := hw.LoopbackTest(nil); err == nil {
		t.Error("LoopbackTest accepted an empty buffer")
	}
	if _, err := hw.LoopbackTest(pattern(hw.FIFO + 1)); err == nil {
		t.Error("LoopbackTest accepted more than one FIFO load, which cannot drain")
	}
}
