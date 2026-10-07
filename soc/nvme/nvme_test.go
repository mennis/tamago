// NVMe controller tests
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package nvme

import (
	"encoding/binary"
	"testing"
	"time"
)

type fakeDMA struct {
	next uint
	bufs map[uint][]byte
	fill byte
}

func newFakeDMA() *fakeDMA { return &fakeDMA{next: 0x100000, bufs: make(map[uint][]byte)} }

func (d *fakeDMA) Reserve(size, align int) (uint, []byte) {
	a := uint(align)
	d.next = (d.next + a - 1) &^ (a - 1)
	addr := d.next
	d.next += uint(size)
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = d.fill
	}
	d.bufs[addr] = buf
	return addr, buf
}
func (d *fakeDMA) Release(addr uint)       { delete(d.bufs, addr) }
func (d *fakeDMA) Bus(addr uint64) uint64  { return 0x10_00000000 + addr }
func (d *fakeDMA) Clean(uint, int)         {}
func (d *fakeDMA) Invalidate(uint, int)    {}
func (d *fakeDMA) Barrier()                {}
func (d *fakeDMA) slice(bus uint64) []byte { return d.bufs[uint(bus-0x10_00000000)] }

type fakeController struct {
	base uint64
	dma  *fakeDMA
	regs map[uint64]uint32

	adminSQ, adminCQ  uint64
	ioSQ, ioCQ        uint64
	adminHead, ioHead int
	flushes           int
	disk              []byte
}

func newFakeController(base uint64, dma *fakeDMA) *fakeController {
	f := &fakeController{base: base, dma: dma, regs: make(map[uint64]uint32), disk: make([]byte, 512*1024)}
	// MQES=15 (16 entries), TO=1 (500 ms), DSTRD=0, MPSMIN=0, CSS.NVM=1.
	f.regs[base+regCAP] = 15 | 1<<24
	f.regs[base+regCAP+4] = 1 << (37 - 32)
	return f
}

func (f *fakeController) Read32(addr uint64) uint32 { return f.regs[addr] }

func (f *fakeController) Write32(addr uint64, value uint32) {
	f.regs[addr] = value
	switch addr - f.base {
	case regCC:
		if value&ccEnable != 0 {
			f.regs[f.base+regCSTS] = cstsReady
		} else {
			f.regs[f.base+regCSTS] = 0
		}
	case regASQ:
		f.adminSQ = (f.adminSQ & 0xffffffff00000000) | uint64(value)
	case regASQ + 4:
		f.adminSQ = (f.adminSQ & 0xffffffff) | uint64(value)<<32
	case regACQ:
		f.adminCQ = (f.adminCQ & 0xffffffff00000000) | uint64(value)
	case regACQ + 4:
		f.adminCQ = (f.adminCQ & 0xffffffff) | uint64(value)<<32
	case doorbellBase:
		f.executeAdmin()
	case doorbellBase + 8:
		f.executeIO()
	}
}

func (f *fakeController) complete(cqBus uint64, head int, cid uint16) {
	cq := f.dma.slice(cqBus)
	off := head * completionSize
	binary.LittleEndian.PutUint16(cq[off+12:], cid)
	binary.LittleEndian.PutUint16(cq[off+14:], 1)
}

func (f *fakeController) executeAdmin() {
	sq := f.dma.slice(f.adminSQ)
	off := f.adminHead * commandSize
	cmd := sq[off : off+commandSize]
	opcode := cmd[0]
	cid := binary.LittleEndian.Uint16(cmd[2:])
	prp1 := binary.LittleEndian.Uint64(cmd[24:])
	switch opcode {
	case adminIdentify:
		buf := f.dma.slice(prp1)
		cns := binary.LittleEndian.Uint32(cmd[40:]) & 0xff
		if cns == identifyController {
			binary.LittleEndian.PutUint32(buf[516:], 1) // NN
		} else {
			binary.LittleEndian.PutUint64(buf[0:], 1024) // NSZE
			buf[26] = 0                                  // FLBAS
			buf[128+2] = 9                               // LBAF0.LBADS = 512 bytes
		}
	case adminCreateIOCQ:
		f.ioCQ = prp1
	case adminCreateIOSQ:
		f.ioSQ = prp1
	}
	f.complete(f.adminCQ, f.adminHead, cid)
	f.adminHead++
}

func (f *fakeController) executeIO() {
	sq := f.dma.slice(f.ioSQ)
	off := f.ioHead * commandSize
	cmd := sq[off : off+commandSize]
	cid := binary.LittleEndian.Uint16(cmd[2:])
	prp1 := binary.LittleEndian.Uint64(cmd[24:])
	if cmd[0] == ioFlush {
		f.flushes++
		f.complete(f.ioCQ, f.ioHead, cid)
		f.ioHead++
		return
	}
	lba := binary.LittleEndian.Uint64(cmd[40:])
	blocks := int(binary.LittleEndian.Uint16(cmd[48:])) + 1
	buf := f.dma.slice(prp1)
	disk := f.disk[int(lba)*512 : (int(lba)+blocks)*512]
	if cmd[0] == ioRead {
		copy(buf, disk)
	} else {
		copy(disk, buf)
	}
	f.complete(f.ioCQ, f.ioHead, cid)
	f.ioHead++
}

func TestInitIdentifyAndBlockIO(t *testing.T) {
	const base = uint64(0x1b_00000000)
	dma := newFakeDMA()
	mmio := newFakeController(base, dma)
	c := &Controller{Base: base, Reg: mmio, DMA: dma, Sleep: func(time.Duration) {}}

	ns, err := c.Init()
	if err != nil {
		t.Fatalf("Controller.Init() error = %v", err)
	}
	if got, want := ns.BlockSize, uint32(512); got != want {
		t.Errorf("Namespace.BlockSize = %d, want %d", got, want)
	}
	if got, want := ns.Blocks, uint64(1024); got != want {
		t.Errorf("Namespace.Blocks = %d, want %d", got, want)
	}

	want := make([]byte, 512)
	for i := range want {
		want[i] = byte(i)
	}
	if err := ns.WriteBlocks(7, want); err != nil {
		t.Fatalf("Namespace.WriteBlocks() error = %v", err)
	}
	got := make([]byte, 512)
	if err := ns.ReadBlocks(7, got); err != nil {
		t.Fatalf("Namespace.ReadBlocks() error = %v", err)
	}
	if string(got) != string(want) {
		t.Error("Namespace.ReadBlocks() data mismatch")
	}
	if err := ns.Flush(); err != nil {
		t.Fatalf("Namespace.Flush() error = %v", err)
	}
	if mmio.flushes != 1 {
		t.Errorf("flush command count = %d, want 1", mmio.flushes)
	}
}

func TestTransferRejectsOutOfRangeAndPartialBlocks(t *testing.T) {
	ns := &Namespace{BlockSize: 512, Blocks: 8}
	if err := ns.ReadBlocks(0, make([]byte, 513)); err == nil {
		t.Error("Namespace.ReadBlocks() accepted a partial block")
	}
	if err := ns.WriteBlocks(8, make([]byte, 512)); err == nil {
		t.Error("Namespace.WriteBlocks() accepted an out-of-range LBA")
	}
}

func TestNewQueueClearsDMA(t *testing.T) {
	dma := newFakeDMA()
	dma.fill = 0xff
	c := &Controller{DMA: dma}
	q := c.newQueue(0, queueDepth, 4)
	for i, b := range q.sq {
		if b != 0 {
			t.Fatalf("submission queue byte %d = %#02x, want 0", i, b)
		}
	}
	for i, b := range q.cq {
		if b != 0 {
			t.Fatalf("completion queue byte %d = %#02x, want 0", i, b)
		}
	}
}
