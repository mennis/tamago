// BCM2712 SoC clock, throttle and temperature support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"encoding/binary"

	"github.com/usbarmory/tamago/soc/bcm2835/mbox"
)

// Unlike the videocore.go accessors, the calls in this file return an error
// when the firmware does not answer, as a zero is a valid rate or flag value.

// property sends one tag carrying req and returns its answered value, which
// must be at least want bytes.
func property(code uint32, req []byte, want int) ([]byte, error) {
	msg := &MailboxMessage{
		Tags: []MailboxTag{{ID: code, Buffer: req}},
	}

	Mailbox.Call(VC_CH_PROPERTYTAGS_A_TO_VC, msg)

	tags := make([]mbox.Tag, len(msg.Tags))
	for i, tag := range msg.Tags {
		tags[i] = mbox.Tag{ID: tag.ID, Buffer: tag.Buffer}
	}

	return mbox.Value(msg.Code, tags, code, want)
}

// clockQuery sends an (id, 0) request for a clock tag and returns the rate
// word of the (id, rate) reply, or an error if the reply is for another clock.
func clockQuery(code uint32, id uint32) (hz uint32, err error) {
	req := make([]byte, 8)
	binary.LittleEndian.PutUint32(req[0:], id)

	buf, err := property(code, req, 8)
	if err != nil {
		return 0, err
	}

	return mbox.Clock(buf, id)
}

// ClockRate returns the rate in Hz the firmware has set for the given clock,
// one of the CLOCK_ID_* constants. This is the last requested rate, not the
// running one (see [ClockRateMeasured]); for the ARM clock the two can differ
// by the whole turbo range during the firmware's initial_turbo window.
//
// The firmware answers an unknown id as another clock (only the low five bits
// are kept, so 99 reads the ARM clock), which is returned as an error. Below 32
// a stopped clock and an id with no clock both read as 0.
func ClockRate(id uint32) (hz uint32, err error) {
	return clockQuery(VC_CLOCK_GET_RATE, id)
}

// MaxClockRate returns the highest rate in Hz the firmware will allow for the
// given clock, the ceiling [SetClockRate] clamps to. An id with no clock is
// answered as itself with a rate of 0.
func MaxClockRate(id uint32) (hz uint32, err error) {
	return clockQuery(VC_CLOCK_GET_MAX_RATE, id)
}

// ClockRateMeasured returns the rate in Hz the firmware measures the given
// clock running at, which unlike [ClockRate] reflects firmware throttling.
//
// The reply does not carry the clock id, so an id the firmware cannot measure
// returns the ARM clock's rate. Validate id with [MaxClockRate] first.
func ClockRateMeasured(id uint32) (hz uint32, err error) {
	req := make([]byte, 8)
	binary.LittleEndian.PutUint32(req[0:], id)

	buf, err := property(VC_CLOCK_GET_RATE_MEASURED, req, 8)
	if err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// SetClockRate requests a rate in Hz for the given clock and returns the rate
// the firmware accepted after clamping and rounding. The skip-turbo word is
// sent as zero, so setting the ARM clock may also set the other turbo clocks.
//
// The returned rate is recorded, not applied: the firmware holds the ARM clock
// at its maximum for initial_turbo seconds after boot (60 when unset), so the
// measured ARM clock does not move until then. Check [ClockRateMeasured], or
// set initial_turbo=0 in config.txt.
//
// A reply for another clock is an error, but only after the exchange, so
// validate id before calling.
func SetClockRate(id uint32, hz uint32) (rate uint32, err error) {
	req := make([]byte, VC_CLOCK_SET_RATE_LEN)
	binary.LittleEndian.PutUint32(req[0:], id)
	binary.LittleEndian.PutUint32(req[4:], hz)
	binary.LittleEndian.PutUint32(req[8:], 0) // do not skip setting turbo

	buf, err := property(VC_CLOCK_SET_RATE, req, 8)
	if err != nil {
		return 0, err
	}

	return mbox.Clock(buf, id)
}

// Throttled returns the firmware throttle word (vcgencmd get_throttled). Bits
// 0-3 report under-voltage, ARM frequency capping, throttling and the soft
// temperature limit as current; bits 16-19 the same since boot.
func Throttled() (flags uint32, err error) {
	buf, err := property(VC_GET_THROTTLED, make([]byte, VC_GET_THROTTLED_LEN), 4)
	if err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// Temperature returns the SoC temperature in millidegrees Celsius.
func Temperature() (milliC uint32, err error) {
	req := make([]byte, VC_TEMP_GET_LEN) // sensor id 0, then the reply word

	buf, err := property(VC_TEMP_GET, req, 8)
	if err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}
