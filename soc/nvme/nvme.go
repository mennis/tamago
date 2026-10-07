// NVMe polled block-device driver
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package nvme implements a polled NVMe 1.x host driver for namespace 1 over a
// single I/O queue pair, without interrupts.
package nvme

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	regCAP  = 0x00
	regCC   = 0x14
	regCSTS = 0x1c
	regAQA  = 0x24
	regASQ  = 0x28
	regACQ  = 0x30

	doorbellBase = 0x1000

	ccEnable  = 1 << 0
	cstsReady = 1 << 0
	cstsFatal = 1 << 1

	commandSize    = 64
	completionSize = 16
	pageSize       = 4096
	queueDepth     = 16

	adminCreateIOSQ = 0x01
	adminCreateIOCQ = 0x05
	adminIdentify   = 0x06

	identifyNamespace  = 0
	identifyController = 1

	ioFlush = 0x00
	ioWrite = 0x01
	ioRead  = 0x02
)

// RegIO abstracts NVMe BAR MMIO.
type RegIO interface {
	Read32(addr uint64) uint32
	Write32(addr uint64, value uint32)
}

// DMA supplies physically contiguous memory and cache maintenance. Bus
// translates a CPU physical address to the address visible to the controller.
type DMA interface {
	Reserve(size, align int) (addr uint, buf []byte)
	Release(addr uint)
	Bus(addr uint64) uint64
	Clean(addr uint, size int)
	Invalidate(addr uint, size int)
	Barrier()
}

type queue struct {
	ctrl       *Controller
	id         uint16
	depth      uint16
	sqAddr     uint
	sq         []byte
	cqAddr     uint
	cq         []byte
	sqTail     uint16
	cqHead     uint16
	cqPhase    uint16
	nextCID    uint16
	doorStride uint64
}

// Controller represents one NVMe PCI function after its BAR and DMA path have
// been enabled by the PCI host driver.
type Controller struct {
	Base  uint64
	Reg   RegIO
	DMA   DMA
	Sleep func(time.Duration)

	admin *queue
	io    *queue
}

// Namespace is namespace 1 exposed as a fixed-size block device.
type Namespace struct {
	BlockSize uint32
	Blocks    uint64
	ctrl      *Controller
}

func (c *Controller) read64(off uint64) uint64 {
	return uint64(c.Reg.Read32(c.Base+off)) | uint64(c.Reg.Read32(c.Base+off+4))<<32
}

func (c *Controller) write64(off, value uint64) {
	c.Reg.Write32(c.Base+off, uint32(value))
	c.Reg.Write32(c.Base+off+4, uint32(value>>32))
}

func (c *Controller) waitReady(want bool, polls int) error {
	for i := 0; i < polls; i++ {
		status := c.Reg.Read32(c.Base + regCSTS)
		if status&cstsFatal != 0 {
			return fmt.Errorf("nvme: controller fatal status %#08x", status)
		}
		if (status&cstsReady != 0) == want {
			return nil
		}
		c.Sleep(time.Millisecond)
	}
	return fmt.Errorf("nvme: controller ready=%t timeout", want)
}

func (c *Controller) newQueue(id uint16, depth uint16, stride uint64) *queue {
	sqAddr, sq := c.DMA.Reserve(int(depth)*commandSize, pageSize)
	cqAddr, cq := c.DMA.Reserve(int(depth)*completionSize, pageSize)
	clear(sq)
	clear(cq)
	c.DMA.Clean(sqAddr, len(sq))
	c.DMA.Clean(cqAddr, len(cq))
	return &queue{ctrl: c, id: id, depth: depth, sqAddr: sqAddr, sq: sq,
		cqAddr: cqAddr, cq: cq, cqPhase: 1, doorStride: stride}
}

func (q *queue) submit(command []byte) (uint32, error) {
	if len(command) != commandSize {
		return 0, errors.New("nvme: command must be 64 bytes")
	}
	cid := q.nextCID
	q.nextCID++
	binary.LittleEndian.PutUint16(command[2:], cid)
	off := int(q.sqTail) * commandSize
	copy(q.sq[off:off+commandSize], command)
	q.ctrl.DMA.Clean(q.sqAddr+uint(off), commandSize)
	q.ctrl.DMA.Barrier()
	q.sqTail++
	if q.sqTail == q.depth {
		q.sqTail = 0
	}
	sqDoorbell := doorbellBase + uint64(2*q.id)*q.doorStride
	q.ctrl.Reg.Write32(q.ctrl.Base+sqDoorbell, uint32(q.sqTail))

	for i := 0; i < 5000; i++ {
		cqOff := int(q.cqHead) * completionSize
		q.ctrl.DMA.Invalidate(q.cqAddr+uint(cqOff), completionSize)
		entry := q.cq[cqOff : cqOff+completionSize]
		status := binary.LittleEndian.Uint16(entry[14:])
		if status&1 == q.cqPhase {
			gotCID := binary.LittleEndian.Uint16(entry[12:])
			if gotCID != cid {
				return 0, fmt.Errorf("nvme: completion CID %d, want %d", gotCID, cid)
			}
			result := binary.LittleEndian.Uint32(entry)
			code := status >> 1
			q.cqHead++
			if q.cqHead == q.depth {
				q.cqHead = 0
				q.cqPhase ^= 1
			}
			cqDoorbell := doorbellBase + uint64(2*q.id+1)*q.doorStride
			q.ctrl.Reg.Write32(q.ctrl.Base+cqDoorbell, uint32(q.cqHead))
			if code != 0 {
				return result, fmt.Errorf("nvme: command %#02x status %#04x", command[0], code)
			}
			return result, nil
		}
		q.ctrl.Sleep(time.Millisecond)
	}
	return 0, fmt.Errorf("nvme: command %#02x timeout", command[0])
}

func (c *Controller) identify(q *queue, nsid uint32, cns uint32) (uint, []byte, error) {
	addr, buf := c.DMA.Reserve(pageSize, pageSize)
	cmd := make([]byte, commandSize)
	cmd[0] = adminIdentify
	binary.LittleEndian.PutUint32(cmd[4:], nsid)
	binary.LittleEndian.PutUint64(cmd[24:], c.DMA.Bus(uint64(addr)))
	binary.LittleEndian.PutUint32(cmd[40:], cns)
	if _, err := q.submit(cmd); err != nil {
		c.DMA.Release(addr)
		return 0, nil, err
	}
	c.DMA.Invalidate(addr, len(buf))
	return addr, buf, nil
}

// Init resets the controller, establishes admin and I/O queues, identifies
// controller namespace 1, and returns its block geometry.
func (c *Controller) Init() (*Namespace, error) {
	if c.Reg == nil || c.DMA == nil {
		return nil, errors.New("nvme: MMIO and DMA are required")
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
	cap := c.read64(regCAP)
	if cap&(1<<37) == 0 {
		return nil, errors.New("nvme: controller does not support the NVM command set")
	}
	minPage := uint64(pageSize) << ((cap >> 48) & 0xf)
	if minPage > pageSize {
		return nil, fmt.Errorf("nvme: minimum page size %d exceeds %d", minPage, pageSize)
	}
	depth := uint16(cap&0xffff) + 1
	if depth > queueDepth {
		depth = queueDepth
	}
	if depth < 2 {
		return nil, errors.New("nvme: controller queue depth is less than 2")
	}
	stride := uint64(4) << ((cap >> 32) & 0xf)
	timeoutPolls := int((cap>>24)&0xff) * 500
	if timeoutPolls == 0 {
		timeoutPolls = 5000
	}

	c.Reg.Write32(c.Base+regCC, 0)
	if err := c.waitReady(false, timeoutPolls); err != nil {
		return nil, err
	}
	c.admin = c.newQueue(0, depth, stride)
	c.Reg.Write32(c.Base+regAQA, uint32(depth-1)|uint32(depth-1)<<16)
	c.write64(regASQ, c.DMA.Bus(uint64(c.admin.sqAddr)))
	c.write64(regACQ, c.DMA.Bus(uint64(c.admin.cqAddr)))
	const ccQueueConfig = 6<<16 | 4<<20
	c.DMA.Barrier()
	c.Reg.Write32(c.Base+regCC, ccEnable|ccQueueConfig)
	if err := c.waitReady(true, timeoutPolls); err != nil {
		return nil, err
	}

	ctrlAddr, ctrlData, err := c.identify(c.admin, 0, identifyController)
	if err != nil {
		return nil, err
	}
	namespaces := binary.LittleEndian.Uint32(ctrlData[516:])
	c.DMA.Release(ctrlAddr)
	if namespaces == 0 {
		return nil, errors.New("nvme: controller reports no namespaces")
	}
	nsAddr, nsData, err := c.identify(c.admin, 1, identifyNamespace)
	if err != nil {
		return nil, err
	}
	blocks := binary.LittleEndian.Uint64(nsData)
	format := nsData[26] & 0xf
	lbaf := 128 + int(format)*4
	metadataSize := binary.LittleEndian.Uint16(nsData[lbaf:])
	lbaShift := nsData[lbaf+2]
	c.DMA.Release(nsAddr)
	if blocks == 0 {
		return nil, errors.New("nvme: namespace has zero blocks")
	}
	if metadataSize != 0 {
		return nil, fmt.Errorf("nvme: namespace metadata size %d is unsupported", metadataSize)
	}
	if lbaShift < 9 || lbaShift > 16 {
		return nil, fmt.Errorf("nvme: namespace LBA shift %d is unsupported", lbaShift)
	}

	c.io = c.newQueue(1, depth, stride)
	createCQ := make([]byte, commandSize)
	createCQ[0] = adminCreateIOCQ
	binary.LittleEndian.PutUint64(createCQ[24:], c.DMA.Bus(uint64(c.io.cqAddr)))
	binary.LittleEndian.PutUint32(createCQ[40:], uint32(1)|uint32(depth-1)<<16)
	binary.LittleEndian.PutUint32(createCQ[44:], 1) // physically contiguous
	if _, err := c.admin.submit(createCQ); err != nil {
		return nil, err
	}
	createSQ := make([]byte, commandSize)
	createSQ[0] = adminCreateIOSQ
	binary.LittleEndian.PutUint64(createSQ[24:], c.DMA.Bus(uint64(c.io.sqAddr)))
	binary.LittleEndian.PutUint32(createSQ[40:], uint32(1)|uint32(depth-1)<<16)
	binary.LittleEndian.PutUint32(createSQ[44:], 1|1<<16) // contiguous, CQID 1
	if _, err := c.admin.submit(createSQ); err != nil {
		return nil, err
	}

	return &Namespace{BlockSize: 1 << lbaShift, Blocks: blocks, ctrl: c}, nil
}

func (ns *Namespace) transfer(opcode byte, lba uint64, buf []byte) error {
	if ns.BlockSize == 0 || len(buf) == 0 || len(buf)%int(ns.BlockSize) != 0 {
		return errors.New("nvme: transfer must contain whole non-empty blocks")
	}
	blocks := uint64(len(buf)) / uint64(ns.BlockSize)
	if blocks > 65536 || lba >= ns.Blocks || blocks > ns.Blocks-lba {
		return errors.New("nvme: transfer is outside namespace")
	}
	if len(buf) > 2*pageSize {
		return errors.New("nvme: transfer exceeds two-page polled limit")
	}
	if ns.ctrl == nil || ns.ctrl.io == nil {
		return errors.New("nvme: namespace is not initialized")
	}
	addr, dmaBuf := ns.ctrl.DMA.Reserve(len(buf), pageSize)
	if opcode == ioWrite {
		copy(dmaBuf, buf)
		ns.ctrl.DMA.Clean(addr, len(dmaBuf))
	} else {
		ns.ctrl.DMA.Invalidate(addr, len(dmaBuf))
	}
	cmd := make([]byte, commandSize)
	cmd[0] = opcode
	binary.LittleEndian.PutUint32(cmd[4:], 1)
	binary.LittleEndian.PutUint64(cmd[24:], ns.ctrl.DMA.Bus(uint64(addr)))
	if len(buf) > pageSize {
		binary.LittleEndian.PutUint64(cmd[32:], ns.ctrl.DMA.Bus(uint64(addr)+pageSize))
	}
	binary.LittleEndian.PutUint64(cmd[40:], lba)
	binary.LittleEndian.PutUint16(cmd[48:], uint16(blocks-1))
	_, err := ns.ctrl.io.submit(cmd)
	if err == nil && opcode == ioRead {
		ns.ctrl.DMA.Invalidate(addr, len(dmaBuf))
		copy(buf, dmaBuf)
	}
	ns.ctrl.DMA.Release(addr)
	return err
}

// ReadBlocks reads complete logical blocks starting at lba. A single transfer
// is currently limited to two 4 KiB pages.
func (ns *Namespace) ReadBlocks(lba uint64, buf []byte) error {
	return ns.transfer(ioRead, lba, buf)
}

// WriteBlocks writes complete logical blocks starting at lba. A successful
// completion does not guarantee persistence; call Flush at a durability
// boundary. A single transfer is currently limited to two 4 KiB pages.
func (ns *Namespace) WriteBlocks(lba uint64, buf []byte) error {
	return ns.transfer(ioWrite, lba, buf)
}

// Flush commits data and metadata previously submitted to this namespace to
// nonvolatile media.
func (ns *Namespace) Flush() error {
	if ns.ctrl == nil || ns.ctrl.io == nil {
		return errors.New("nvme: namespace is not initialized")
	}
	cmd := make([]byte, commandSize)
	cmd[0] = ioFlush
	binary.LittleEndian.PutUint32(cmd[4:], 1)
	_, err := ns.ctrl.io.submit(cmd)
	return err
}
