// Cadence GEM (macb) Ethernet MAC driver for the Raspberry Pi 5 RP1
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package gem

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDMA models DMA memory as a single array, with the RP1 bus offset and
// no-op cache maintenance.
type fakeDMA struct {
	mem      []byte
	next     uint
	reserves int
	releases int

	// barrierHook, if set, runs on each Barrier to model a DMA write
	// landing at that instant.
	barriers    int
	barrierHook func()
}

const fakeBusOffset = 0x10_00000000

func newFakeDMA() *fakeDMA { return &fakeDMA{mem: make([]byte, 1<<20), next: 0x1000} }

func (d *fakeDMA) Reserve(size, align int) (uint, []byte) {
	d.reserves++
	a := uint(align)
	d.next = (d.next + a - 1) &^ (a - 1)
	addr := d.next
	d.next += uint(size)
	return addr, d.mem[addr : addr+uint(size)]
}

func (d *fakeDMA) ReserveCoherent(size, align int) (uint, []byte) { return d.Reserve(size, align) }

func (d *fakeDMA) Release(addr uint)         { d.releases++ }
func (d *fakeDMA) Bus(cpu uint64) uint64     { return fakeBusOffset + cpu }
func (d *fakeDMA) Clean(addr uint, size int) {}
func (d *fakeDMA) Invalidate(uint, int)      {}
func (d *fakeDMA) Barrier() {
	d.barriers++
	if d.barrierHook != nil {
		d.barrierHook()
	}
}

// slice returns the descriptor/buffer memory at a CPU-physical address.
func (d *fakeDMA) slice(addr uint, n int) []byte { return d.mem[addr : addr+uint(n)] }

// fakeReg models the GEM registers, with NSR always idle and MDIO reads
// answered from phy.
type fakeReg struct {
	// MDIO and the data path run under different driver locks
	mu   sync.Mutex
	regs map[uint64]uint32
	phy  map[uint32]uint16 // PHY registers keyed by pa<<5|ra
	base uint64

	mdioNeverIdle bool // forces the MDIO timeout
}

func newFakeReg(base uint64) *fakeReg {
	return &fakeReg{
		regs: map[uint64]uint32{base + NSR: nsrIDLE},
		phy:  make(map[uint32]uint16),
		base: base,
	}
}

func (r *fakeReg) Read32(addr uint64) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mdioNeverIdle && addr == r.base+NSR {
		return 0
	}
	return r.regs[addr]
}
func (r *fakeReg) Write32(addr uint64, v uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if addr == r.base+MAN && v&manRead == manRead {
		// clause-22 read, DATA is replaced with the PHY register
		pa := (v >> 23) & 0x1f
		ra := (v >> 18) & 0x1f
		v = v&^0xffff | uint32(r.phy[pa<<5|ra])
	}
	r.regs[addr] = v
	r.regs[r.base+NSR] = nsrIDLE
}

const testBase = 0x1f00100000

func newTestGEM() (*GEM, *fakeReg, *fakeDMA) {
	reg := newFakeReg(testBase)
	d := newFakeDMA()
	hw := &GEM{
		Base:     testBase,
		Reg:      reg,
		DMA:      d,
		RingSize: 4,
		MAC:      net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55},
	}
	return hw, reg, d
}

func TestManFrame(t *testing.T) {
	// Read of PHY addr 1, reg 0x19.
	got := manFrame(manRead, 1, 0x19, 0)
	want := uint32(1<<30 | 2<<28 | 1<<23 | 0x19<<18 | 2<<16)
	if got != want {
		t.Fatalf("read frame = %#x, want %#x", got, want)
	}

	// Write of 0x1234 to PHY addr 3, reg 4.
	got = manFrame(manWrite, 3, 4, 0x1234)
	want = uint32(1<<30 | 1<<28 | 3<<23 | 4<<18 | 2<<16 | 0x1234)
	if got != want {
		t.Fatalf("write frame = %#x, want %#x", got, want)
	}
}

func TestMDIOTransport(t *testing.T) {
	hw, reg, _ := newTestGEM()

	// Seed PHY addr 1, reg 2 (PHY ID1) with the Broadcom OUI half.
	reg.phy[1<<5|2] = 0x600d
	data, ok := hw.ReadPHY(1, 2)
	if !ok || data != 0x600d {
		t.Fatalf("ReadPHY = %#x ok=%v, want 0x600d true", data, ok)
	}

	if ok := hw.WritePHY(1, 0, 0x1140); !ok {
		t.Fatal("WritePHY not ok")
	}
	if got := reg.regs[testBase+MAN]; got != manFrame(manWrite, 1, 0, 0x1140) {
		t.Fatalf("MAN after write = %#x, want %#x", got, manFrame(manWrite, 1, 0, 0x1140))
	}
}

func TestRingNextWrap(t *testing.T) {
	r := &ring{size: 3}
	seq := []struct {
		idx  int
		wrap bool
	}{
		{1, false}, {2, false}, {0, true}, {1, false},
	}
	for step, s := range seq {
		if w := r.next(); w != s.wrap || r.index != s.idx {
			t.Fatalf("step %d: index=%d wrap=%v, want index=%d wrap=%v",
				step, r.index, w, s.idx, s.wrap)
		}
	}
}

func TestRingInitOwnership(t *testing.T) {
	d := newFakeDMA()

	tx := &ring{}
	tx.init(d, 4, false)
	for i := 0; i < 4; i++ {
		ctrl := tx.word(i, 1)
		if ctrl&txUsed == 0 {
			t.Fatalf("tx desc %d not USED (software-owned) at init", i)
		}
		if wrap := ctrl&txWrap != 0; wrap != (i == 3) {
			t.Fatalf("tx desc %d wrap=%v, want %v", i, wrap, i == 3)
		}
		// buffer address must be a bus address
		lo := tx.word(i, 0)
		hi := tx.word(i, 2)
		bus := uint64(hi)<<32 | uint64(lo)
		if bus>>32 == 0 {
			t.Fatalf("tx desc %d addr %#x lacks the 64-bit bus offset", i, bus)
		}
	}

	rx := &ring{}
	rx.init(d, 4, true)
	for i := 0; i < 4; i++ {
		addr := rx.word(i, 0)
		if addr&rxUsed != 0 {
			t.Fatalf("rx desc %d USED at init (should be hardware-owned)", i)
		}
		if wrap := addr&rxWrap != 0; wrap != (i == 3) {
			t.Fatalf("rx desc %d wrap=%v, want %v", i, wrap, i == 3)
		}
	}
}

func TestInitConfiguresMAC(t *testing.T) {
	hw, reg, _ := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}

	if got := reg.regs[testBase+DMACFG]; got&dmacfgADDR64 == 0 {
		t.Fatalf("DMACFG = %#x, ADDR64 not set", got)
	}
	// full RX/TX packet buffer partition
	if got := reg.regs[testBase+DMACFG]; got&dmacfgRXBMSFull != dmacfgRXBMSFull || got&dmacfgTXPBMS == 0 {
		t.Fatalf("DMACFG = %#x, want RXBMS full + TXPBMS", got)
	}
	if got := reg.regs[testBase+NCFGR]; got&ncfgrMDCMask != ncfgrMDCDiv96 {
		t.Fatalf("NCFGR MDC field = %#x, want %#x", got&ncfgrMDCMask, ncfgrMDCDiv96)
	}
	if got := reg.regs[testBase+NCFGR]; got&ncfgrGBE == 0 || got&ncfgrFD == 0 {
		t.Fatalf("NCFGR = %#x, want GBE|FD (default 1000FD)", got)
	}
	if got := reg.regs[testBase+NCR]; got&ncrMPE == 0 {
		t.Fatalf("NCR = %#x, MPE not set", got)
	}
	// SA1B = octets 0-3 little-endian, SA1T = octets 4-5.
	if got := reg.regs[testBase+SA1B]; got != 0x33221102 {
		t.Fatalf("SA1B = %#x, want 0x33221102", got)
	}
	if got := reg.regs[testBase+SA1T]; got != 0x5544 {
		t.Fatalf("SA1T = %#x, want 0x5544", got)
	}
}

func TestSetLink(t *testing.T) {
	hw, reg, _ := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}

	hw.SetLink(100, false)
	cfg := reg.regs[testBase+NCFGR]
	if cfg&ncfgrGBE != 0 || cfg&ncfgrFD != 0 || cfg&ncfgrSPD == 0 {
		t.Fatalf("NCFGR = %#x, want SPD only (100 half-duplex)", cfg)
	}

	hw.SetLink(10, true)
	cfg = reg.regs[testBase+NCFGR]
	if cfg&ncfgrGBE != 0 || cfg&ncfgrSPD != 0 || cfg&ncfgrFD == 0 {
		t.Fatalf("NCFGR = %#x, want FD only (10 full-duplex)", cfg)
	}
}

func TestTransmitAndReclaim(t *testing.T) {
	hw, reg, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}

	frame := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 2, 1, 2, 3, 4, 5, 0x88, 0xb5, 'h', 'i'}
	if err := hw.Transmit(frame); err != nil {
		t.Fatal(err)
	}

	// descriptor 0, padded to the minimum length
	ctrl := binary.LittleEndian.Uint32(d.slice(hw.tx.descAddr+4, 4))
	if ctrl&txUsed != 0 {
		t.Fatal("tx desc 0 still USED after Transmit (should be hardware-owned)")
	}
	if ctrl&txLast == 0 {
		t.Fatal("tx desc 0 missing LAST")
	}
	if got := int(ctrl & txLen); got != minFrameSize {
		t.Fatalf("tx length = %d, want %d (short frame padded)", got, minFrameSize)
	}
	if got := reg.regs[testBase+NCR]; got&ncrTSTART == 0 {
		t.Fatal("TSTART not asserted")
	}

	// hardware completion
	binary.LittleEndian.PutUint32(d.slice(hw.tx.descAddr+4, 4), ctrl|txUsed)

	// fill the ring and wrap back onto the reclaimed slot 0
	for i := 0; i < 4; i++ {
		if err := hw.Transmit(frame); err != nil {
			t.Fatalf("Transmit %d: %v", i, err)
		}
		// hardware completion
		off := hw.tx.descAddr + uint((hw.tx.index+hw.tx.size-1)%hw.tx.size)*descSize + 4
		c := binary.LittleEndian.Uint32(d.slice(off, 4))
		binary.LittleEndian.PutUint32(d.slice(off, 4), c|txUsed)
	}
}

func TestTransmitRingFull(t *testing.T) {
	hw, _, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}

	frame := make([]byte, 60)
	// fill every slot without completion
	for i := 0; i < hw.tx.size; i++ {
		if err := hw.Transmit(frame); err != nil {
			t.Fatalf("Transmit %d unexpectedly failed: %v", i, err)
		}
		// in flight
		off := hw.tx.descAddr + uint(i)*descSize + 4
		c := binary.LittleEndian.Uint32(d.slice(off, 4))
		binary.LittleEndian.PutUint32(d.slice(off, 4), c&^uint32(txUsed))
	}
	if err := hw.Transmit(frame); err == nil {
		t.Fatal("Transmit into a full ring should fail")
	}
}

func TestTransmitTooLarge(t *testing.T) {
	hw, _, _ := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Transmit(make([]byte, maxFrameSize)); err == nil {
		t.Fatal("oversized frame should be rejected")
	}
}

func TestReceive(t *testing.T) {
	hw, _, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}

	// nothing received yet
	if n, err := hw.Receive(make([]byte, 128)); n != 0 || err != nil {
		t.Fatalf("Receive on empty ring = %d, %v; want 0, nil", n, err)
	}

	// hardware delivers 60 bytes + FCS into descriptor 0
	payload := 60
	buf := d.slice(hw.rx.bufAddr, payload)
	for i := range buf {
		buf[i] = byte(i)
	}
	addr := binary.LittleEndian.Uint32(d.slice(hw.rx.descAddr, 4))
	binary.LittleEndian.PutUint32(d.slice(hw.rx.descAddr, 4), addr|rxUsed)
	binary.LittleEndian.PutUint32(d.slice(hw.rx.descAddr+4, 4),
		uint32((payload+fcsLen)&rxLenMask)|rxSOF|rxEOF)

	out := make([]byte, 128)
	n, err := hw.Receive(out)
	if err != nil {
		t.Fatal(err)
	}
	if n != payload {
		t.Fatalf("Receive n = %d, want %d (FCS stripped)", n, payload)
	}
	for i := 0; i < n; i++ {
		if out[i] != byte(i) {
			t.Fatalf("byte %d = %#x, want %#x", i, out[i], byte(i))
		}
	}

	// descriptor returned to hardware, ring advanced
	if a := binary.LittleEndian.Uint32(d.slice(hw.rx.descAddr, 4)); a&rxUsed != 0 {
		t.Fatal("rx desc 0 still USED after Receive (not returned to hardware)")
	}
	if hw.rx.index != 1 {
		t.Fatalf("rx index = %d, want 1", hw.rx.index)
	}
}

// TestReadRXDescBarrierOrdersLengthAfterOwnership checks that readRXDesc
// places its barrier between the word0 and word1 loads, by landing a
// hardware writeback at the barrier.
func TestReadRXDescBarrierOrdersLengthAfterOwnership(t *testing.T) {
	d := newFakeDMA()
	r := &ring{}
	r.init(d, 4, true)

	const freshLen = 128
	const i = 0
	d.barriers = 0 // ignore init's barrier
	d.barrierHook = func() {
		r.setWord(i, 0, r.word(i, 0)|rxUsed)
		r.setWord(i, 1, freshLen)
	}

	addr, status := r.readRXDesc(d, i)

	if addr&rxUsed != 0 {
		t.Fatalf("word0 read after the barrier (addr=%#x): the read barrier must sit between the word0 and word1 loads", addr)
	}
	if status != freshLen {
		t.Fatalf("length = %d, want %d: word1 was read before the barrier — the stale-length RX coherence bug", status, freshLen)
	}
	if d.barriers != 1 {
		t.Fatalf("readRXDesc issued %d barriers, want exactly 1", d.barriers)
	}
}

// deliverRX models the hardware receiving a frame into rx descriptor i.
func deliverRX(d *fakeDMA, rx *ring, i, payload int) {
	buf := d.slice(rx.bufAddr+uint(i*rxBufLen), payload)
	for j := range buf {
		buf[j] = byte(j)
	}
	off := rx.descAddr + uint(i)*descSize
	addr := binary.LittleEndian.Uint32(d.slice(off, 4))
	binary.LittleEndian.PutUint32(d.slice(off, 4), addr|rxUsed)
	binary.LittleEndian.PutUint32(d.slice(off+4, 4),
		uint32((payload+fcsLen)&rxLenMask)|rxSOF|rxEOF)
}

func TestReceiveSkipsRunt(t *testing.T) {
	hw, _, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}

	// a runt must be skipped, not reported as an empty ring
	deliverRX(d, &hw.rx, 0, 0)
	deliverRX(d, &hw.rx, 1, 40)

	n, err := hw.Receive(make([]byte, 128))
	if err != nil {
		t.Fatal(err)
	}
	if n != 40 {
		t.Fatalf("Receive skipping a runt = %d, want 40 (the following frame)", n)
	}
	if hw.rx.index != 2 {
		t.Fatalf("rx index = %d, want 2 (runt + frame consumed)", hw.rx.index)
	}
}

func TestStopStartNoLeak(t *testing.T) {
	hw, _, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}
	firstReserves := d.reserves

	// stop/start cycles must not leak ring allocations
	for i := 0; i < 3; i++ {
		hw.Stop()
		if err := hw.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.reserves - d.releases; got != firstReserves {
		t.Fatalf("live reservations = %d, want %d (stop/start leaked)", got, firstReserves)
	}
}

func TestHandleIRQ(t *testing.T) {
	hw, reg, _ := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}

	var tx, rx int
	hw.TXHandler = func() { tx++ }
	hw.RXHandler = func() { rx++ }

	// TX and RX completion
	reg.regs[testBase+ISR] = intTCOMP | intRCOMP
	status := hw.HandleIRQ()
	if status&intTCOMP == 0 || status&intRCOMP == 0 {
		t.Fatalf("HandleIRQ status = %#x, want TCOMP|RCOMP", status)
	}
	if tx != 1 || rx != 1 {
		t.Fatalf("handlers tx=%d rx=%d, want 1,1", tx, rx)
	}
	// ISR acknowledged
	if got := reg.regs[testBase+ISR]; got != (intTCOMP | intRCOMP) {
		t.Fatalf("ISR W1C write = %#x, want %#x", got, intTCOMP|intRCOMP)
	}

	// no interrupt asserted
	reg.regs[testBase+ISR] = 0
	if hw.HandleIRQ() != 0 {
		t.Fatal("HandleIRQ with no cause should return 0")
	}
	if tx != 1 || rx != 1 {
		t.Fatalf("handlers fired without a cause: tx=%d rx=%d", tx, rx)
	}
}

func TestInitValidation(t *testing.T) {
	if err := (&GEM{}).Init(); err == nil {
		t.Fatal("Init with no Base/Reg/DMA should fail")
	}
	hw := &GEM{Base: testBase, Reg: newFakeReg(testBase), DMA: newFakeDMA(), MAC: net.HardwareAddr{1, 2, 3}}
	if err := hw.Init(); err == nil {
		t.Fatal("Init with a short MAC should fail")
	}
}

// startedGEM returns a running GEM ready to receive.
func startedGEM(t *testing.T) (*GEM, *fakeReg, *fakeDMA) {
	t.Helper()
	hw, reg, d := newTestGEM()
	if err := hw.Init(); err != nil {
		t.Fatal(err)
	}
	if err := hw.Start(); err != nil {
		t.Fatal(err)
	}
	return hw, reg, d
}

// TestRXLenMaskExcludesOffset checks that the RX_OFFSET bits [13:12] are not
// decoded as frame length.
func TestRXLenMaskExcludesOffset(t *testing.T) {
	hw, _, d := startedGEM(t)

	payload := 60
	buf := d.slice(hw.rx.bufAddr, payload)
	for i := range buf {
		buf[i] = byte(i)
	}
	off := hw.rx.descAddr
	addr := binary.LittleEndian.Uint32(d.slice(off, 4))
	binary.LittleEndian.PutUint32(d.slice(off, 4), addr|rxUsed)
	// length plus an RX_OFFSET bit
	status := uint32(payload+fcsLen) | (1 << 12) | rxSOF | rxEOF
	binary.LittleEndian.PutUint32(d.slice(off+4, 4), status)

	n, err := hw.Receive(make([]byte, rxBufLen))
	if err != nil {
		t.Fatal(err)
	}
	if n != payload {
		t.Fatalf("Receive n = %d, want %d — RX_OFFSET bit 12 leaked into the length", n, payload)
	}
}

// TestReceiveShortBuffer checks that a frame larger than buf is not truncated
// but kept for a retry with a larger buffer.
func TestReceiveShortBuffer(t *testing.T) {
	hw, _, d := startedGEM(t)
	deliverRX(d, &hw.rx, 0, 60)
	idxBefore := hw.rx.index

	n, err := hw.Receive(make([]byte, 16))
	if err == nil {
		t.Fatalf("Receive into a 16-byte buffer returned (%d, nil); want an error", n)
	}
	if n != 0 {
		t.Fatalf("Receive n = %d on error, want 0", n)
	}
	if hw.rx.index != idxBefore {
		t.Fatalf("rx index advanced on a too-small buffer (frame consumed): %d -> %d", idxBefore, hw.rx.index)
	}

	if n, err := hw.Receive(make([]byte, rxBufLen)); err != nil || n != 60 {
		t.Fatalf("retry with a big buffer = (%d, %v), want (60, nil)", n, err)
	}
}

// TestReceiveNilBuffer checks that Receive(nil) does not consume frames.
func TestReceiveNilBuffer(t *testing.T) {
	hw, _, d := startedGEM(t)
	deliverRX(d, &hw.rx, 0, 60)
	deliverRX(d, &hw.rx, 1, 60)

	if n, err := hw.Receive(nil); n != 0 || err == nil {
		t.Fatalf("Receive(nil) = (%d, %v); want (0, error) without consuming", n, err)
	}
	if n, err := hw.Receive(make([]byte, rxBufLen)); err != nil || n != 60 {
		t.Fatalf("after Receive(nil), Receive = (%d, %v), want (60, nil) — frames were destroyed", n, err)
	}
}

// TestMDIODoesNotBlockTransmit checks that Transmit completes while ReadPHY
// waits for its timeout.
func TestMDIODoesNotBlockTransmit(t *testing.T) {
	hw, reg, _ := startedGEM(t)
	reg.mdioNeverIdle = true

	phyDone := make(chan struct{})
	go func() {
		hw.ReadPHY(1, 2)
		close(phyDone)
	}()

	// let ReadPHY enter its poll loop
	time.Sleep(2 * time.Millisecond)

	txDone := make(chan error, 1)
	go func() { txDone <- hw.Transmit(make([]byte, 64)) }()

	select {
	case err := <-txDone:
		if err != nil {
			t.Fatalf("Transmit: %v", err)
		}
	case <-phyDone:
		t.Fatal("ReadPHY finished before Transmit — Transmit was blocked on the MDIO lock")
	case <-time.After(2 * time.Second):
		t.Fatal("Transmit did not return")
	}
	<-phyDone
}
