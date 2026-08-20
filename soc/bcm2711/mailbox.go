// BCM2711 VideoCore mailbox support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/usbarmory/tamago/soc/bcm2835/mbox"
)

// ResponseSuccess is the code the firmware writes into a message header when it
// has processed every tag. The mbox codec does not export it.
const ResponseSuccess = 0x80000000

// The VPU mailboxes live in the ARMC block, and are not the ARM_LOCAL
// core-to-core mailboxes the datasheet's Chapter 13 documents. Broadcom does
// not document these at all, so the layout below is the raspberrypi/firmware
// wiki's, confirmed on a Raspberry Pi 4 Model B.
//
//	Mailbox  Read/Write  Peek  Sender  Status  Config
//	   0     0x00        0x10  0x14    0x18    0x1c
//	   1     0x20        0x30  0x34    0x38    0x3c
//
// Mailbox 0 is VC->ARM and mailbox 1 is ARM->VC. The ARM must never write MB0
// or read MB1.
const (
	MAILBOX0_READ   = 0x00
	MAILBOX0_STATUS = 0x18
	MAILBOX1_WRITE  = 0x20
	MAILBOX1_STATUS = 0x38

	MAILBOX_FULL  = 0x80000000
	MAILBOX_EMPTY = 0x40000000
)

// Mailbox channels.
const (
	CH_PROPERTYTAGS_A_TO_VC = 8
	CH_PROPERTYTAGS_VC_TO_A = 9
)

// mailboxSpinLimit bounds every status poll. A mailbox that never answers must
// produce an error rather than spin, as an unbounded wait on a board with no
// operator present is indistinguishable from a crash.
const mailboxSpinLimit = 1 << 24

// ErrMailboxTimeout is returned when the VideoCore does not answer.
var ErrMailboxTimeout = errors.New("bcm2711: mailbox timeout")

// ErrMailboxRefused is returned when the firmware answers but does not mark the
// message successful.
var ErrMailboxRefused = errors.New("bcm2711: mailbox request refused")

// MailboxTag is a single property-tag request or response.
type MailboxTag struct {
	ID     uint32
	Buffer []byte
}

// MailboxMessage is one property-tag exchange.
type MailboxMessage struct {
	Code    uint32
	Tags    []MailboxTag
	MinSize int
}

// Tag returns the response tag with the given id, or nil.
func (m *MailboxMessage) Tag(code uint32) *MailboxTag {
	for i, t := range m.Tags {
		if t.ID == code {
			return &m.Tags[i]
		}
	}
	return nil
}

// Error reports whether the firmware refused the message.
func (m *MailboxMessage) Error() bool {
	return m.Code != ResponseSuccess
}

type mailbox struct {
	sync.Mutex

	// dirty records that an exchange gave up with a request outstanding, so
	// the answer may arrive afterwards and sit ahead of the next caller's.
	//
	// Draining on a mismatched address does not cover it, as the address is
	// not a message identity: the buffer is an ordinary heap allocation, so a
	// later call can be handed the same address and accept the previous call's
	// values as its own. The timeout is therefore remembered rather than
	// inferred, and the next exchange clears the queue first.
	dirty bool
}

// Mailbox provides access to the BCM2711 mailbox used to communicate with the
// VideoCore.
var Mailbox = &mailbox{}

// Call performs one property tag exchange.
//
// The message buffer is an ordinary heap allocation rather than a reserved DMA
// region, which works because the identity map makes a Go pointer the physical
// address the VideoCore needs, and the RAM region sits inside the one gigabyte
// window the GPU alias reaches. A fixed low memory region would be worse here,
// the obvious choice of 0xC000 lands inside the page tables.
func (mb *mailbox) Call(channel int, message *MailboxMessage) error {
	mb.Lock()
	defer mb.Unlock()

	tags := make([]mbox.Tag, len(message.Tags))
	for i, t := range message.Tags {
		tags[i] = mbox.Tag{ID: t.ID, Buffer: t.Buffer}
	}

	size := mbox.RequestSize(tags, message.MinSize)

	// The buffer is aligned and padded to a whole cache line, not merely to
	// the 16 bytes the mailbox needs, because the invalidation below is a pure
	// discard over whole lines. A buffer sharing its first or last line with a
	// neighbouring heap object would have that neighbour's dirty data thrown
	// away rather than written back.
	//
	// This is not hypothetical: a single 4-byte tag makes a 28-byte message,
	// which lands in a 48-byte size class and shares a line with whatever sits
	// beside it.
	const line = 64 // arm64 assumes a 64-byte line; see tamago #127

	backing := make([]byte, size+2*line)
	base := uintptr(unsafe.Pointer(&backing[0]))
	off := (line - (base & (line - 1))) & (line - 1)

	buf := backing[off : off+uintptr(size)]
	addr := base + off

	// Maintenance covers the padded, line-aligned span; the codec sees only the
	// message. Every line touched lies inside backing, so no other object can
	// share one.
	span := uintptr((size + line - 1) &^ (line - 1))

	mbox.Encode(buf, tags)
	ARM.CleanDataCacheRange(addr, span)

	err := mb.exchange(channel, uint32(addr))

	ARM.InvalidateDataCacheRange(addr, span)

	// KeepAlive so the collector cannot reclaim the buffer while the VideoCore
	// is still reading it — the GC has no idea another processor holds a
	// pointer to this memory.
	runtime.KeepAlive(backing)

	if err != nil {
		return err
	}

	code, respTags, err := mbox.Decode(buf)
	if err != nil {
		return err
	}

	message.Code = code
	message.Tags = make([]MailboxTag, len(respTags))
	for i, t := range respTags {
		message.Tags[i] = MailboxTag{ID: t.ID, Buffer: t.Buffer}
	}

	if message.Error() {
		return ErrMailboxRefused
	}
	return nil
}

// exchange writes one message address and waits for its acknowledgement.
//
// addr is a physical address deliberately: the property tags channel is the
// documented exception to the bus address rule, and a Raspberry Pi 4 was
// measured accepting the physical form.
func (mb *mailbox) exchange(channel int, addr uint32) error {
	if addr&0xf != 0 {
		return errors.New("bcm2711: mailbox message must be 16-byte aligned")
	}

	status := PeripheralAddress(MailboxOffset + MAILBOX1_STATUS)
	write := PeripheralAddress(MailboxOffset + MAILBOX1_WRITE)
	rstatus := PeripheralAddress(MailboxOffset + MAILBOX0_STATUS)
	read := PeripheralAddress(MailboxOffset + MAILBOX0_READ)

	// Clear anything a previous timeout left behind BEFORE asking, so a late
	// reply cannot be mistaken for this one. See mailbox.dirty: matching on the
	// address is not enough, because the heap can hand a later call the same
	// buffer and a stale reply then matches perfectly.
	if mb.dirty {
		for spins := 0; Read32(rstatus)&MAILBOX_EMPTY == 0; spins++ {
			if spins > mailboxSpinLimit {
				// Draining did not converge, which means something is
				// still filling MB0. Stay dirty rather than pretending the
				// queue is clean.
				return ErrMailboxTimeout
			}

			_ = Read32(read)
		}

		mb.dirty = false
	}

	// Wait for MB1 to have room. Note this polls MAILBOX1's status: the FULL
	// bit that matters before a write belongs to the mailbox being written.
	// soc/bcm2712 polls mailbox 0's for both and survives only because MB1's
	// eight-deep FIFO never fills with one message in flight.
	for spins := 0; Read32(status)&MAILBOX_FULL != 0; spins++ {
		if spins > mailboxSpinLimit {
			// Nothing was written, so nothing is outstanding and the queue
			// stays clean.
			return ErrMailboxTimeout
		}
	}

	Write32(write, (addr&^0xf)|uint32(channel&0xf))

	for spins := 0; ; spins++ {
		if spins > mailboxSpinLimit {
			// The request HAS been posted and no answer arrived in time. The
			// VideoCore may still answer, so the next exchange must drain
			// before it asks.
			mb.dirty = true

			return ErrMailboxTimeout
		}
		if Read32(rstatus)&MAILBOX_EMPTY != 0 {
			continue
		}

		data := Read32(read)
		if data&0xf != uint32(channel&0xf) {
			// Another channel's traffic. Discard and keep waiting rather than
			// decoding whatever that channel left behind.
			continue
		}
		if data&^0xf != addr&^0xf {
			// A reply for a different message, so discard it and keep
			// waiting. This is the recovery path for a previous timeout:
			// returning an error here instead made one transient timeout
			// permanent, as each call would then fail on the previous
			// call's queued reply. The spin limit still applies, so a
			// genuinely absent answer times out as before.
			continue
		}
		return nil
	}
}
