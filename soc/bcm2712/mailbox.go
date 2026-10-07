// BCM2712 SoC Mailbox support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/usbarmory/tamago/dma"
	"github.com/usbarmory/tamago/soc/bcm2835/mbox"
)

const (
	MAILBOX_REGION_BASE = 0xC000
	MAILBOX_REGION_SIZE = 0x4000
)

const (
	MAILBOX_BASE       = 0x13880
	MAILBOX_READ_REG   = MAILBOX_BASE + 0x00
	MAILBOX_STATUS_REG = MAILBOX_BASE + 0x18
	MAILBOX_WRITE_REG  = MAILBOX_BASE + 0x20
	MAILBOX_FULL       = 0x80000000
	MAILBOX_EMPTY      = 0x40000000
)

type mailbox struct {
	sync.Mutex

	Region *dma.Region
}

// Mailbox provides access to the BCM2712 mailbox used to communicate with
// the VideoCore CPU
var Mailbox = mailbox{}

func init() {
	var err error

	if Mailbox.Region, err = dma.NewRegion(MAILBOX_REGION_BASE, MAILBOX_REGION_SIZE, false); err != nil {
		panic("bcm2712: mailbox region init failed: " + err.Error())
	}
}

// MailboxTag represents a single tag in a mailbox message
type MailboxTag struct {
	ID     uint32
	Buffer []byte
}

// MailboxMessage represents a mailbox message
type MailboxMessage struct {
	MinSize int
	Code    uint32
	Tags    []MailboxTag
}

// Error returns true if the mailbox response indicates an error
func (m *MailboxMessage) Error() bool {
	return m.Code == 0x80000001
}

// Tag returns the tag matching the given code
func (m *MailboxMessage) Tag(code uint32) *MailboxTag {
	for i, tag := range m.Tags {
		if tag.ID&0x7FFFFFFF == code&0x7FFFFFFF {
			return &m.Tags[i]
		}
	}

	return nil
}

// Call exchanges message via a mailbox channel
func (mb *mailbox) Call(channel int, message *MailboxMessage) {
	// the DMA region and the mailbox registers are shared by every caller
	mb.Lock()
	defer mb.Unlock()

	// the wire format is handled by the mbox package
	tags := make([]mbox.Tag, len(message.Tags))
	for i, tag := range message.Tags {
		tags[i] = mbox.Tag{ID: tag.ID, Buffer: tag.Buffer}
	}

	size := mbox.RequestSize(tags, message.MinSize)

	addr, buf := mb.Region.Reserve(size, 16)
	defer mb.Region.Release(addr)

	mbox.Encode(buf, tags)
	ARM.CleanDataCacheRange(uintptr(addr), uintptr(size))

	mb.exchangeMessage(channel, uint32(addr))
	ARM.InvalidateDataCacheRange(uintptr(addr), uintptr(size))

	code, respTags, err := mbox.Decode(buf)
	if err != nil {
		panic("malformed mailbox response: " + err.Error())
	}

	message.Code = code
	message.Tags = make([]MailboxTag, len(respTags))
	for i, tag := range respTags {
		message.Tags[i] = MailboxTag{ID: tag.ID, Buffer: tag.Buffer}
	}
}

func (mb *mailbox) exchangeMessage(channel int, addr uint32) {
	if (addr & 0xF) != 0 {
		panic("Mailbox message must be 16-byte aligned")
	}

	busAddr := addr + GPU_BUS_OFFSET

	for (Read32(SoCPeripheralAddress(MAILBOX_STATUS_REG)) & MAILBOX_FULL) != 0 {
		runtime.Gosched()
	}

	Write32(SoCPeripheralAddress(MAILBOX_WRITE_REG), uint32(channel&0xF)|(busAddr&0xFFFFFFF0))

	for (Read32(SoCPeripheralAddress(MAILBOX_STATUS_REG)) & MAILBOX_EMPTY) != 0 {
		runtime.Gosched()
	}

	data := Read32(SoCPeripheralAddress(MAILBOX_READ_REG))

	if (data & 0xF) != uint32(channel&0xF) {
		panic(fmt.Sprintf("overlapping messages, got response for channel %d, expecting %d", data&0xF, channel&0xF))
	}
	if (data & 0xFFFFFFF0) != (busAddr & 0xFFFFFFF0) {
		panic(fmt.Sprintf("overlapping messages, got response for addr 0x%x, expecting 0x%x", data&0xFFFFFFF0, busAddr&0xFFFFFFF0))
	}
}
