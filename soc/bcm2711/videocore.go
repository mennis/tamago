// BCM2711 VideoCore property interface
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"encoding/binary"
	"errors"
)

// ErrTagMissing is returned when the firmware marks a message successful but
// does not echo back the tag that was asked for. This is not the same failure
// as ErrMailboxRefused: the exchange worked and the VideoCore simply dropped
// the request, which is how it answers a tag this firmware does not implement.
var ErrTagMissing = errors.New("bcm2711: property tag missing from response")

// ErrShortResponse is returned when a tag comes back with fewer value bytes
// than the tag's definition requires. The response length is the firmware's,
// not ours — the codec sizes each response buffer from the reply — so this
// means the firmware answered a different shape than documented rather than
// that the request buffer was too small.
var ErrShortResponse = errors.New("bcm2711: property tag response too short")

// property performs a single-tag exchange on the property-tags channel and
// returns the response value buffer, which is at least min bytes long.
//
// The tag is looked up by identifier afterwards rather than read out of the
// slice handed in, as Call replaces the tags wholesale with what the firmware
// sent and the reply may carry fewer than the request did.
func property(id uint32, buf []byte, min int) ([]byte, error) {
	msg := &MailboxMessage{
		Tags: []MailboxTag{
			{
				ID:     id,
				Buffer: buf,
			},
		},
	}

	if err := Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return nil, err
	}

	tag := msg.Tag(id)

	if tag == nil {
		return nil, ErrTagMissing
	}

	if len(tag.Buffer) < min {
		return nil, ErrShortResponse
	}

	return tag.Buffer, nil
}

// FirmwareRevision returns the VideoCore firmware revision, which is a build
// stamp rather than an ordered version: compare it for equality against a known
// good boot, not with an inequality.
//
// Note that soc/bcm2835's same-named accessor sends the board revision tag and
// returns a different number under this name. This one sends the tag it is
// named for.
func FirmwareRevision() (rev uint32, err error) {
	buf, err := property(GET_FIRMWARE_REVISION, make([]byte, GET_FIRMWARE_REVISION_LEN), 4)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// BoardModel returns the board model code, which is zero, and zero is not an
// error: no Raspberry Pi measured here populates this tag, answering with a
// successful message code and a well formed but meaningless reply. Use
// [BoardRevision], which really does encode the model.
func BoardModel() (model uint32, err error) {
	buf, err := property(GET_BOARD_MODEL, make([]byte, GET_BOARD_MODEL_LEN), 4)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// BoardRevision returns the board revision code, the usual way to identify the
// hardware. New style codes, with bit 23 set, pack the model into bits [11:4]
// and the total RAM size class into bits [22:20], which is worth knowing
// because [ARMMemory] cannot express it.
func BoardRevision() (rev uint32, err error) {
	buf, err := property(GET_BOARD_REVISION, make([]byte, GET_BOARD_REVISION_LEN), 4)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// BoardSerial returns the board's 64-bit serial number. The low 32 bits are
// what the bootloader uses as its network boot directory prefix and what Linux
// prints as the cpuinfo serial; the whole value is returned so a caller can
// pick.
func BoardSerial() (serial uint64, err error) {
	buf, err := property(GET_BOARD_SERIAL, make([]byte, GET_BOARD_SERIAL_LEN), 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint64(buf), nil
}

// MACAddress returns the board's Ethernet MAC address as six bytes in wire
// order. It is reported by the firmware whether or not any Ethernet driver has
// been brought up, and asking for it does not touch the MAC hardware. The
// returned slice is freshly allocated per call.
func MACAddress() (addr []byte, err error) {
	buf, err := property(GET_BOARD_MAC_ADDRESS, make([]byte, GET_BOARD_MAC_ADDRESS_LEN), 6)

	if err != nil {
		return
	}

	addr = make([]byte, 6)
	copy(addr, buf)

	return
}

// ARMMemory returns the base and size of the memory the firmware has assigned
// to the ARM cores, which is not a description of the board's RAM: both fields
// are 32-bit and the DRAM is not contiguous, so this reports the low bank
// alone and a larger board cannot be sized from it. Use [BoardRevision]'s size
// class for the total.
//
// The low bank is the interesting one regardless, as it is the only memory the
// GPU alias reaches and therefore the only memory a mailbox buffer or
// framebuffer may live in.
func ARMMemory() (base uint32, size uint32, err error) {
	buf, err := property(GET_ARM_MEMORY, make([]byte, GET_ARM_MEMORY_LEN), 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[0:]), binary.LittleEndian.Uint32(buf[4:]), nil
}

// VCMemory returns the base and size of the VideoCore carve-out.
//
// This has to be asked for and must never be hard-coded. The size is whatever
// gpu_mem in config.txt says, so it changes with the contents of the boot
// partition and not with the silicon; the bench Pi 4 measured
// [0x3b400000, 0x40000000) but a different config.txt on the same board moves
// that boundary. It is worth two things to a caller: it is memory the ARM must
// not write, and its base is where the usable low bank ends.
func VCMemory() (base uint32, size uint32, err error) {
	buf, err := property(GET_VC_MEMORY, make([]byte, GET_VC_MEMORY_LEN), 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[0:]), binary.LittleEndian.Uint32(buf[4:]), nil
}

// Temperature returns the SoC temperature in thousandths of a degree Celsius.
// It is the only thermal source available here, as GET_GENCMD_RESULT is
// non-functional on this firmware, see messages.go.
func Temperature() (millidegrees uint32, err error) {
	buf := make([]byte, GET_TEMPERATURE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], 0) // sensor id, only 0 exists

	buf, err = property(GET_TEMPERATURE, buf, 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// Throttle flags, as GET_THROTTLED returns them. The low four bits are
// conditions holding now and the same four at bits 16-19 are sticky, meaning
// the condition has occurred since boot.
const (
	ThrottledUnderVoltage  = 1 << 0
	ThrottledFreqCapped    = 1 << 1
	ThrottledThrottled     = 1 << 2
	ThrottledSoftTempLimit = 1 << 3

	ThrottledUnderVoltageSince  = 1 << 16
	ThrottledFreqCappedSince    = 1 << 17
	ThrottledThrottledSince     = 1 << 18
	ThrottledSoftTempLimitSince = 1 << 19
)

// Throttled returns the firmware's throttling word, which answers a question no
// other reading on this SoC can: whether a measurement was taken on a part
// running at full speed. A slow frame on a throttled board and one on a slow
// code path look identical from inside the program.
//
// The sticky half matters more than the live half, as the under-voltage that
// caused a doubtful number may be over by the time anybody reads it. Zero is a
// real answer here rather than an ambiguous one.
func Throttled() (flags uint32, err error) {
	buf := make([]byte, GET_THROTTLED_LEN)

	buf, err = property(GET_THROTTLED, buf, 4)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[0:]), nil
}
