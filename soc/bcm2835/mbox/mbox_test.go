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
	"strings"
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

// Value accepts only an answered tag in a successful message.
func TestValue(t *testing.T) {
	const id = 0x00030047
	answered := []Tag{{ID: id, Buffer: []byte{3, 0, 0, 0, 0x80, 0xd1, 0xf0, 0x08}}}

	v, err := Value(ResponseSuccess, answered, id, 8)
	if err != nil || len(v) != 8 || v[4] != 0x80 {
		t.Fatalf("Value(success, answered) = %v, %v; want the 8-byte value", v, err)
	}

	for _, tc := range []struct {
		name string
		code uint32
		tags []Tag
		want string
	}{
		{"firmware error", ResponseError, answered, "response code 0x80000001"},
		{"unprocessed", 0, answered, "response code 0x00000000"},
		{"missing tag", ResponseSuccess, []Tag{{ID: 0x00030002, Buffer: make([]byte, 8)}}, "missing"},
		{"short answer", ResponseSuccess, []Tag{{ID: id, Buffer: make([]byte, 4)}}, "answered 4 bytes, want 8"},
		{"no answer", ResponseSuccess, []Tag{{ID: id, Buffer: nil}}, "answered 0 bytes"},
	} {
		if _, err := Value(tc.code, tc.tags, id, 8); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Value error = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

// A response with the request bit set on the tag ID still matches the request.
func TestValueIgnoresResponseBit(t *testing.T) {
	tags := []Tag{{ID: 0x80030046, Buffer: []byte{0, 0, 8, 0}}}
	if _, err := Value(ResponseSuccess, tags, 0x00030046, 4); err != nil {
		t.Errorf("Value with response bit set on tag ID: %v", err)
	}
}

// clockValue builds an (id, rate) clock tag value as the firmware writes it.
func clockValue(id, hz uint32) []byte {
	v := make([]byte, 8)
	binary.LittleEndian.PutUint32(v, id)
	binary.LittleEndian.PutUint32(v[4:], hz)
	return v
}

// Clock returns a rate only for the requested clock, the firmware answers an
// unknown id as a different clock (e.g. 99 as 3, the ARM clock).
func TestClock(t *testing.T) {
	if hz, err := Clock(clockValue(3, 1_500_000_000), 3); err != nil || hz != 1_500_000_000 {
		t.Fatalf("Clock(answered, 3) = %d, %v; want 1500000000", hz, err)
	}

	// a stopped clock is not an error
	if hz, err := Clock(clockValue(15, 0), 15); err != nil || hz != 0 {
		t.Fatalf("Clock(stopped, 15) = %d, %v; want 0, nil", hz, err)
	}

	for _, tc := range []struct {
		name  string
		value []byte
		id    uint32
		want  string
	}{
		// firmware responses
		{"99 masked to ARM", clockValue(3, 1_500_000_000), 99, "clock 99 answered as clock 3"},
		{"0xffff masked to 0x1f", clockValue(0x1f, 0), 0xffff, "clock 65535 answered as clock 31"},
		{"all-ones id word", clockValue(0xffffffff, 1_500_000_000), 15, "answered as clock 4294967295"},
		{"short", make([]byte, 4), 3, "4 bytes, want 8"},
	} {
		if hz, err := Clock(tc.value, tc.id); err == nil || hz != 0 || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Clock = %d, %v; want 0 and an error containing %q", tc.name, hz, err, tc.want)
		}
	}
}
