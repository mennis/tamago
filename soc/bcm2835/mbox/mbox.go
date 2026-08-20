// VideoCore mailbox property interface codec
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package mbox implements serialization of the VideoCore mailbox property
// interface. It has no hardware, DMA or GOOS=tamago dependency, so that the
// wire format can be tested on the host; a SoC package wraps it with the DMA
// buffer, cache maintenance and register exchange.
//
// Message layout (all fields little-endian, see
// https://github.com/raspberrypi/firmware/wiki/Mailbox-property-interface):
//
//	u32 total size in bytes
//	u32 request/response code
//	repeated tags:
//		u32 tag id
//		u32 value buffer size in bytes
//		u32 request/response indicator + response size
//		value buffer, zero-padded to a 4-byte boundary
//	u32 end tag (0)
package mbox

import (
	"encoding/binary"
	"fmt"
)

// Tag is a mailbox property-interface tag: a 32-bit identifier and its value
// buffer.
type Tag struct {
	ID     uint32
	Buffer []byte
}

const (
	headerSize = 8  // total size + code
	tagHeader  = 12 // id + buffer size + response indicator
	endTag     = 4  // terminating zero tag
)

// pad4 rounds n up to the next multiple of four, the mailbox value-buffer
// alignment.
func pad4(n int) int {
	return (n + 3) &^ 3
}

// RequestSize returns the serialized byte size of a request carrying tags,
// raised to minSize. It is the size the caller must allocate before Encode.
func RequestSize(tags []Tag, minSize int) int {
	size := headerSize
	for _, t := range tags {
		size += tagHeader + pad4(len(t.Buffer))
	}
	size += endTag

	if size < minSize {
		size = minSize
	}

	return size
}

// Encode serializes tags into buf as a mailbox request. buf must be at least
// RequestSize(tags, 0) bytes long; its length is written as the message's total
// size field, so callers should pass a slice sized by RequestSize.
func Encode(buf []byte, tags []Tag) {
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(buf)))
	binary.LittleEndian.PutUint32(buf[4:], 0) // process request

	off := headerSize

	for _, t := range tags {
		binary.LittleEndian.PutUint32(buf[off:], t.ID)
		binary.LittleEndian.PutUint32(buf[off+4:], uint32(len(t.Buffer)))
		binary.LittleEndian.PutUint32(buf[off+8:], 0) // request: response size 0
		copy(buf[off+12:], t.Buffer)

		off += tagHeader + pad4(len(t.Buffer))
	}

	binary.LittleEndian.PutUint32(buf[off:], 0) // end tag
}

// Decode parses a mailbox response buffer, returning the response code and the
// tags it carries. Unlike a hardware panic it reports malformed input as an
// error, so both callers and tests can exercise the truncated- and over-sized-
// tag paths.
func Decode(buf []byte) (code uint32, tags []Tag, err error) {
	if len(buf) < headerSize {
		return 0, nil, fmt.Errorf("mbox: response too short (%d bytes)", len(buf))
	}

	code = binary.LittleEndian.Uint32(buf[4:])

	// The end tag is a single u32, so only four bytes are required to detect
	// it; a real tag header needs twelve. Checking the end tag first lets a
	// tightly-packed response terminate cleanly instead of being rejected as
	// truncated when its end tag lands at the very end of the buffer.
	for off := headerSize; off+4 <= len(buf); {
		id := binary.LittleEndian.Uint32(buf[off:])
		if id == 0 {
			break
		}

		if off+tagHeader > len(buf) {
			return code, tags, fmt.Errorf("mbox: truncated tag header at offset %d", off)
		}

		bufSize := binary.LittleEndian.Uint32(buf[off+4:])
		respSize := binary.LittleEndian.Uint32(buf[off+8:]) & 0x7fffffff

		if bufSize > uint32(len(buf)-off-tagHeader) {
			return code, tags, fmt.Errorf("mbox: over-sized tag buffer (%d) at offset %d", bufSize, off)
		}
		if respSize > bufSize {
			return code, tags, fmt.Errorf("mbox: response value (%d) exceeds tag buffer (%d) at offset %d", respSize, bufSize, off)
		}

		value := make([]byte, respSize)
		copy(value, buf[off+tagHeader:])
		tags = append(tags, Tag{ID: id, Buffer: value})

		off += tagHeader + pad4(int(bufSize))
	}

	return code, tags, nil
}
