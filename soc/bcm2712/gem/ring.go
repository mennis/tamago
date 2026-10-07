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
	"errors"
)

// ADDR64 descriptors are four little-endian words: address low,
// control/status, address high, reserved.
const descSize = 16

// Transmit descriptor control word (word1) flags
const (
	txLen  = 0x3fff  // frame length [13:0] (GEM 14-bit)
	txLast = 1 << 15 // last buffer of the frame
	txWrap = 1 << 30 // last descriptor in the ring
	txUsed = 1 << 31 // ownership: 1 = software (free), 0 = hardware
)

// Receive descriptor flags, ownership and wrap are in the low bits of the
// address word (word0), the length in the status word (word1).
const (
	rxUsed    = 1 << 0 // ownership: 1 = software (filled), 0 = hardware
	rxWrap    = 1 << 1 // last descriptor in the ring
	rxLenMask = 0xfff  // frame length [11:0], includes FCS
	rxSOF     = 1 << 14
	rxEOF     = 1 << 15
)

// ring represents a descriptor ring, in coherent memory (see
// [DMA.ReserveCoherent]), and its cacheable data buffers.
type ring struct {
	size  int
	index int

	descAddr uint // CPU-physical base of the descriptor block (coherent)
	descBuf  []byte
	bufAddr  uint // CPU-physical base of the data-buffer block (cacheable)
	dataBuf  []byte
}

// init allocates and lays out the ring, TX descriptors start owned by software
// and RX ones by hardware. A previous allocation is released first.
func (r *ring) init(d DMA, size int, rx bool) {
	if r.descBuf != nil {
		d.Release(r.descAddr)
		d.Release(r.bufAddr)
	}

	r.size = size
	r.index = 0

	r.descAddr, r.descBuf = d.ReserveCoherent(size*descSize, bufAlign)
	r.bufAddr, r.dataBuf = d.Reserve(size*rxBufLen, bufAlign)

	for i := 0; i < size; i++ {
		bus := d.Bus(uint64(r.bufAddr) + uint64(i*rxBufLen))
		last := i == size-1

		if rx {
			lo := uint32(bus) &^ uint32(rxUsed|rxWrap) // HW owns (USED=0)
			if last {
				lo |= rxWrap
			}
			r.writeDesc(i, lo, 0, uint32(bus>>32))
		} else {
			ctrl := uint32(txUsed) // SW owns (free)
			if last {
				ctrl |= txWrap
			}
			r.writeDesc(i, uint32(bus), ctrl, uint32(bus>>32))
		}
	}

	// order ring writes before queue pointer programming
	d.Barrier()
}

// writeDesc writes descriptor i.
func (r *ring) writeDesc(i int, lo, ctrl, hi uint32) {
	b := r.descBuf[i*descSize : i*descSize+descSize]
	binary.LittleEndian.PutUint32(b[0:], lo)
	binary.LittleEndian.PutUint32(b[4:], ctrl)
	binary.LittleEndian.PutUint32(b[8:], hi)
	binary.LittleEndian.PutUint32(b[12:], 0)
}

// word reads word n (0-3) of descriptor i.
func (r *ring) word(i, n int) uint32 {
	return binary.LittleEndian.Uint32(r.descBuf[i*descSize+n*4:])
}

// readRXDesc reads the address word (word0) then the status word (word1) of
// receive descriptor i. ARMv8 may reorder loads from Normal Non-Cacheable
// memory, so a barrier between the two prevents reading a stale length for a
// just delivered frame (as dma_rmb() in Linux macb).
func (r *ring) readRXDesc(d DMA, i int) (addr, status uint32) {
	addr = r.word(i, 0)
	d.Barrier()
	status = r.word(i, 1)
	return
}

// setWord writes word n (0-3) of descriptor i.
func (r *ring) setWord(i, n int, v uint32) {
	binary.LittleEndian.PutUint32(r.descBuf[i*descSize+n*4:], v)
}

// data returns the buffer slice for descriptor i.
func (r *ring) data(i int) []byte {
	return r.dataBuf[i*rxBufLen : i*rxBufLen+rxBufLen]
}

// next advances the ring index with wrap, returning true when it wraps.
func (r *ring) next() (wrapped bool) {
	if r.index == r.size-1 {
		r.index = 0
		return true
	}
	r.index++
	return false
}

// Transmit transmits an Ethernet frame, excluding the FCS which is appended by
// the MAC. An error is returned if the transmit ring is full.
func (hw *GEM) Transmit(frame []byte) error {
	if len(frame) > maxFrameSize-fcsLen {
		return errors.New("gem: frame too large")
	}

	hw.Lock()
	defer hw.Unlock()

	if !hw.started {
		return errors.New("gem: not started")
	}

	r := &hw.tx
	i := r.index

	// the slot is free once the hardware sets USED
	hw.DMA.Barrier()
	if r.word(i, 1)&txUsed == 0 {
		return errors.New("gem: transmit ring full")
	}

	buf := r.data(i)
	n := copy(buf, frame)
	for j := n; j < minFrameSize; j++ {
		buf[j] = 0 // pad short frames to the minimum
	}
	length := n
	if length < minFrameSize {
		length = minFrameSize
	}
	hw.DMA.Clean(r.bufAddr+uint(i*rxBufLen), length)

	// single buffer frame, USED cleared to pass ownership to hardware
	ctrl := uint32(length&txLen) | txLast
	if i == r.size-1 {
		ctrl |= txWrap
	}
	r.setWord(i, 1, ctrl)
	hw.DMA.Barrier()

	hw.w(NCR, hw.r(NCR)|ncrTSTART)

	r.next()
	return nil
}

// Receive copies the next received Ethernet frame, excluding the FCS, into buf
// and returns its length, which is zero when no frame is pending. Runt frames
// are skipped.
//
// A frame larger than buf is not truncated: an error is returned and the frame
// is kept, so that it can be read again with a larger buffer.
func (hw *GEM) Receive(buf []byte) (n int, err error) {
	hw.Lock()
	defer hw.Unlock()

	if !hw.started {
		return 0, errors.New("gem: not started")
	}

	r := &hw.rx

	hw.DMA.Barrier()

	for {
		i := r.index

		addr, status := r.readRXDesc(hw.DMA, i)
		if addr&rxUsed == 0 {
			return 0, nil // owned by hardware
		}

		if int(status&rxLenMask) <= fcsLen {
			// skip runts, so they do not look like an empty ring
			r.recycleRX(hw.DMA, i, addr)
			continue
		}
		length := int(status&rxLenMask) - fcsLen

		if length > len(buf) {
			// keep the descriptor for a retry
			return 0, errors.New("gem: receive buffer smaller than frame")
		}

		hw.DMA.Invalidate(r.bufAddr+uint(i*rxBufLen), length)
		copy(buf[:length], r.data(i)[:length])

		r.recycleRX(hw.DMA, i, addr)
		return length, nil
	}
}

// recycleRX returns receive descriptor i to the hardware and advances the ring.
func (r *ring) recycleRX(d DMA, i int, addr uint32) {
	r.setWord(i, 0, addr&^uint32(rxUsed))
	d.Barrier()
	r.next()
}
