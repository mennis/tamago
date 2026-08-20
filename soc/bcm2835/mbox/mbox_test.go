// BCM2712 mailbox property-interface codec
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package mbox

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestRequestSize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tags    []Tag
		minSize int
		want    int
	}{
		{"empty", nil, 0, 12}, // 8 header + 4 end tag
		{"one aligned tag", []Tag{{ID: 1, Buffer: make([]byte, 4)}}, 0, 28},
		{"one unaligned tag padded up", []Tag{{ID: 1, Buffer: make([]byte, 3)}}, 0, 28},
		{"unaligned to next word", []Tag{{ID: 1, Buffer: make([]byte, 5)}}, 0, 32},
		{"two tags", []Tag{{ID: 1, Buffer: make([]byte, 4)}, {ID: 2, Buffer: make([]byte, 8)}}, 0, 48},
		{"raised to minSize", []Tag{{ID: 1, Buffer: make([]byte, 4)}}, 64, 64},
		{"minSize below natural size ignored", []Tag{{ID: 1, Buffer: make([]byte, 4)}}, 8, 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestSize(tc.tags, tc.minSize); got != tc.want {
				t.Fatalf("RequestSize = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	tags := []Tag{{ID: 0x10, Buffer: []byte{1, 2, 3}}}
	size := RequestSize(tags, 0)
	buf := make([]byte, size)

	Encode(buf, tags)

	want := []byte{
		0x1c, 0, 0, 0, // total size = 28
		0, 0, 0, 0, // request code
		0x10, 0, 0, 0, // tag id
		0x03, 0, 0, 0, // value buffer size = 3
		0, 0, 0, 0, // request: response size 0
		1, 2, 3, 0, // value, zero-padded to 4 bytes
		0, 0, 0, 0, // end tag
	}

	if !bytes.Equal(buf, want) {
		t.Fatalf("Encode = % x\nwant             % x", buf, want)
	}
}

// response builds a canned VideoCore response buffer carrying tags whose value
// buffers are already filled with response data. extraSlack appends trailing
// zero bytes (as a real MinSize-padded transaction would) after the end tag.
func response(code uint32, tags []Tag, extraSlack int) []byte {
	size := RequestSize(tags, 0) + extraSlack
	buf := make([]byte, size)

	binary.LittleEndian.PutUint32(buf[0:], uint32(size))
	binary.LittleEndian.PutUint32(buf[4:], code)

	off := headerSize
	for _, tg := range tags {
		binary.LittleEndian.PutUint32(buf[off:], tg.ID)
		binary.LittleEndian.PutUint32(buf[off+4:], uint32(len(tg.Buffer)))
		// response indicator set, response size == value length
		binary.LittleEndian.PutUint32(buf[off+8:], 0x80000000|uint32(len(tg.Buffer)))
		copy(buf[off+12:], tg.Buffer)
		off += tagHeader + pad4(len(tg.Buffer))
	}

	return buf
}

func TestDecodeResponse(t *testing.T) {
	tags := []Tag{
		{ID: 0x10, Buffer: []byte{0xaa, 0xbb, 0xcc, 0xdd}},
		{ID: 0x20, Buffer: []byte{1, 2, 3}}, // unaligned, padded on the wire
	}

	for _, slack := range []int{0, 8, 32} {
		t.Run("", func(t *testing.T) {
			buf := response(0x80000000, tags, slack)

			code, got, err := Decode(buf)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if code != 0x80000000 {
				t.Fatalf("code = %#x, want 0x80000000", code)
			}
			if !reflect.DeepEqual(got, tags) {
				t.Fatalf("tags = %+v, want %+v", got, tags)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// A request encodes value sizes but response size 0, so a round trip with
	// the value buffers echoed back as the response models a completed
	// transaction. Slack mirrors a MinSize-padded buffer.
	tags := []Tag{{ID: 0x38, Buffer: []byte{9, 8, 7, 6}}}

	buf := response(0x80000000, tags, 16)

	_, got, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, tags) {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, tags)
	}
}

func TestDecodeMalformed(t *testing.T) {
	// valid two-tag response we then corrupt in various ways
	base := response(0x80000000, []Tag{{ID: 0x10, Buffer: []byte{1, 2, 3, 4}}}, 0)

	t.Run("short buffer", func(t *testing.T) {
		if _, _, err := Decode(base[:4]); err == nil {
			t.Fatal("expected error for buffer shorter than header")
		}
	})

	t.Run("truncated tag header", func(t *testing.T) {
		// keep header + a partial (non-zero) tag header, no end tag
		bad := make([]byte, headerSize+6)
		copy(bad, base[:headerSize])
		binary.LittleEndian.PutUint32(bad[headerSize:], 0x10) // non-zero id
		if _, _, err := Decode(bad); err == nil {
			t.Fatal("expected error for truncated tag header")
		}
	})

	t.Run("over-sized tag buffer", func(t *testing.T) {
		bad := append([]byte(nil), base...)
		// claim a value buffer far larger than the remaining bytes
		binary.LittleEndian.PutUint32(bad[headerSize+4:], 0xffff)
		if _, _, err := Decode(bad); err == nil {
			t.Fatal("expected error for over-sized tag buffer")
		}
	})

	t.Run("response value exceeds tag buffer", func(t *testing.T) {
		bad := append([]byte(nil), base...)
		// value buffer size 4 but response claims 8 bytes returned
		binary.LittleEndian.PutUint32(bad[headerSize+8:], 0x80000000|8)
		if _, _, err := Decode(bad); err == nil {
			t.Fatal("expected error for response value exceeding tag buffer")
		}
	})
}
