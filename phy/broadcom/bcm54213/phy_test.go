// BCM54213PE Ethernet PHY support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm54213

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usbarmory/tamago/phy"
)

var _ phy.PHY = &PHY{}

// model is a scripted BCM54213PE register map covering the latched-low link
// bit, the auxiliary control shadow select and write enable mechanics, and
// the shadow register select and write strobe mechanics.
type model struct {
	addr int // the address the PHY answers on, others read all ones

	id1, id2 uint16
	control  uint16
	auxstat  uint16

	misc     uint16            // auxiliary control MISC shadow
	miscRead uint16            // auxiliary control read select, last written
	shd      map[uint16]uint16 // shadow registers keyed by select
	shdSel   uint16            // shadow select, last written without the strobe

	quiet  bool // floating bus, every read returns all ones
	sticky bool // shadow writes are silently ignored

	anegAfter   int // status reads before auto-negotiation completes
	reads       int
	latchedDown bool // link bit latched low until read once while up
}

func newModel() *model {
	return &model{
		addr:        1,
		id1:         0x600d,
		id2:         0x84a2,
		auxstat:     0xff3c, // HCD 7, 1000 Mbps full duplex
		misc:        0x71e7 &^ (1 << AUX_RGMII_SKEW),
		shd:         map[uint16]uint16{0x3: 0x0000},
		anegAfter:   3,
		latchedDown: true,
	}
}

func (m *model) ReadPHYRegister(pa int, ra int) (uint16, error) {
	if m.quiet || pa != m.addr {
		return silent, nil
	}

	switch ra {
	case BASIC_CONTROL:
		return m.control, nil
	case BASIC_STATUS:
		m.reads++

		var status uint16

		if m.reads > m.anegAfter {
			status |= 1 << STATUS_ANEG_COMPLETE

			if m.latchedDown {
				m.latchedDown = false
			} else {
				status |= 1 << STATUS_LINK
			}
		}

		return status, nil
	case PHY_ID_1:
		return m.id1, nil
	case PHY_ID_2:
		return m.id2, nil
	case PHY_AUX_CONTROL:
		if m.miscRead == AUX_MISC {
			return m.misc, nil
		}

		return 0, nil
	case PHY_AUX_STATUS:
		return m.auxstat, nil
	case PHY_SHADOW:
		return m.shdSel<<10 | m.shd[m.shdSel], nil
	}

	return 0, nil
}

func (m *model) WritePHYRegister(pa int, ra int, data uint16) error {
	if m.quiet || pa != m.addr {
		return nil
	}

	switch ra {
	case BASIC_CONTROL:
		m.control = data

		if data&(1<<CTRL_ANEG_RESTART) != 0 {
			m.reads = 0
			m.latchedDown = true
		}
	case PHY_AUX_CONTROL:
		m.miscRead = (data >> 12) & 0x7

		// MISC only accepts data with the write enable set, a select for
		// read must leave it untouched.
		if data&0x7 == AUX_MISC && data&(1<<AUX_WRITE_ENABLE) != 0 && !m.sticky {
			m.misc = data &^ (1 << AUX_WRITE_ENABLE)
		}
	case PHY_SHADOW:
		if data&(1<<SHD_WRITE) == 0 {
			m.shdSel = (data >> 10) & 0x1f
		} else if !m.sticky {
			m.shd[(data>>10)&0x1f] = data & SHD_DATA
		}
	}

	return nil
}

func newPHY(t *testing.T, m *model, mode Mode) *PHY {
	t.Helper()

	hw := &PHY{
		Mode:  mode,
		Sleep: func(time.Duration) {},
	}

	if err := hw.Init(1, m); err != nil {
		t.Fatalf("Init: %v", err)
	}

	return hw
}

func TestInit(t *testing.T) {
	hw := &PHY{}

	if err := hw.Init(1, nil); err == nil {
		t.Fatal("Init accepted a nil transport")
	}

	if err := hw.Init(32, newModel()); err == nil {
		t.Fatal("Init accepted an out of range address")
	}

	if err := hw.Init(1, newModel()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if hw.Address() != 1 {
		t.Errorf("Address %d, want 1", hw.Address())
	}

	if hw.Timeout != Timeout {
		t.Errorf("Timeout %v, want %v", hw.Timeout, Timeout)
	}
}

func TestIdentifier(t *testing.T) {
	m := newModel()
	hw := newPHY(t, m, RGMIIID)

	id, err := hw.Identifier()

	if err != nil {
		t.Fatalf("Identifier: %v", err)
	}

	if id != 0x600d84a2 {
		t.Fatalf("Identifier %#08x, want 0x600d84a2", id)
	}

	// a later stepping must still be accepted
	m.id2 = 0x84a5

	if _, err = hw.Identifier(); err != nil {
		t.Fatalf("Identifier: %v", err)
	}

	// a different part must not
	m.id2 = 0x84e0

	if _, err = hw.Identifier(); err == nil {
		t.Fatal("Identifier accepted a foreign PHY")
	}
}

func TestIdentifierSilent(t *testing.T) {
	m := newModel()
	m.quiet = true
	hw := newPHY(t, m, RGMIIID)

	if _, err := hw.Identifier(); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Fatalf("Identifier on a floating bus: %v, want a silent PHY diagnosis", err)
	}

	// an unpopulated address reads all ones as well
	m.quiet = false
	m.addr = 2

	if _, err := hw.Identifier(); err == nil {
		t.Fatal("Identifier at the wrong address succeeded")
	}
}

func TestReset(t *testing.T) {
	var events []string

	hw := newPHY(t, newModel(), RGMIIID)
	hw.ResetLine = func(assert bool) { events = append(events, fmt.Sprintf("reset(%v)", assert)) }
	hw.Sleep = func(d time.Duration) { events = append(events, fmt.Sprintf("sleep(%v)", d)) }

	if err := hw.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	want := []string{"reset(true)", "sleep(20ms)", "reset(false)", "sleep(150ms)"}

	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("reset sequence %v, want %v", events, want)
	}

	hw.ResetLine = nil

	if err := hw.Reset(); err == nil {
		t.Fatal("Reset without a reset line succeeded")
	}
}

func TestClockDelay(t *testing.T) {
	for _, tt := range []struct {
		mode  Mode
		skew  bool
		delay bool
	}{
		{RGMII, false, false},
		{RGMIIID, true, true},
		{RGMIIRXID, true, false},
		{RGMIITXID, false, true},
	} {
		// from both the reset state and the enabled state, so that the
		// clearing paths are exercised too
		for _, preset := range []bool{false, true} {
			m := newModel()

			if preset {
				m.misc |= 1 << AUX_RGMII_SKEW
				m.shd[0x3] |= 1 << SHD_GTXCLK_DELAY
			}

			hw := newPHY(t, m, tt.mode)

			if err := hw.SetClockDelay(); err != nil {
				t.Fatalf("mode %d preset %v: %v", tt.mode, preset, err)
			}

			skew, delay, err := hw.ClockDelay()

			if err != nil {
				t.Fatalf("mode %d preset %v: %v", tt.mode, preset, err)
			}

			if skew != tt.skew || delay != tt.delay {
				t.Errorf("mode %d preset %v: skew %v delay %v, want %v %v",
					tt.mode, preset, skew, delay, tt.skew, tt.delay)
			}
		}
	}
}

// TestClockDelayPreserves confirms the configuration is a read-modify-write:
// a bit the driver does not own must survive it.
func TestClockDelayPreserves(t *testing.T) {
	const other = 1 << 4

	m := newModel()
	m.misc |= other
	hw := newPHY(t, m, RGMIIID)

	if err := hw.SetClockDelay(); err != nil {
		t.Fatalf("SetClockDelay: %v", err)
	}

	if m.misc&other == 0 {
		t.Error("SetClockDelay cleared a MISC bit it does not own")
	}
}

func TestClockDelayFailure(t *testing.T) {
	// all ones carries both enable bits and must not read as configured
	m := newModel()
	m.quiet = true
	hw := newPHY(t, m, RGMIIID)

	if err := hw.SetClockDelay(); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Fatalf("floating bus: %v, want a silent PHY diagnosis", err)
	}

	// writes that do not stick must fail the read back
	m = newModel()
	m.sticky = true
	hw = newPHY(t, m, RGMIIID)

	if err := hw.SetClockDelay(); err == nil || !strings.Contains(err.Error(), "take effect") {
		t.Fatalf("ignored writes: %v, want a verification failure", err)
	}
}

func TestNegotiate(t *testing.T) {
	m := newModel()
	hw := newPHY(t, m, RGMIIID)

	if err := hw.Negotiate(); err != nil {
		t.Fatalf("Negotiate: %v", err)
	}

	want := uint16((1 << CTRL_ANEG_ENABLE) | (1 << CTRL_ANEG_RESTART))

	if m.control != want {
		t.Fatalf("control %#04x, want %#04x", m.control, want)
	}

	if hw.Speed() != 0 {
		t.Errorf("Speed %d, want 0 before the link resolves", hw.Speed())
	}
}

func TestLink(t *testing.T) {
	m := newModel()
	m.anegAfter = 0
	hw := newPHY(t, m, RGMIIID)

	// The link bit is latched low, a single read would report the stale
	// state left by the reset.
	up, err := hw.Link()

	if err != nil {
		t.Fatalf("Link: %v", err)
	}

	if !up {
		t.Error("Link down, want up after the latch clears")
	}

	m.quiet = true

	if _, err = hw.Link(); err == nil {
		t.Fatal("Link on a floating bus succeeded")
	}
}

func TestStatus(t *testing.T) {
	for _, tt := range []struct {
		hcd    uint16
		speed  int
		duplex bool
	}{
		{7, 1000, true},
		{6, 1000, false},
		{5, 100, true},
		{3, 100, false},
		{2, 10, true},
		{1, 10, false},
	} {
		m := newModel()
		m.anegAfter = 0
		m.auxstat = tt.hcd << AUX_HCD
		hw := newPHY(t, m, RGMIIID)

		status, err := hw.Status()

		if err != nil {
			t.Fatalf("hcd %d: %v", tt.hcd, err)
		}

		if status.Speed != tt.speed || status.FullDuplex != tt.duplex {
			t.Errorf("hcd %d: %d Mbps duplex %v, want %d %v",
				tt.hcd, status.Speed, status.FullDuplex, tt.speed, tt.duplex)
		}

		if hw.Speed() != tt.speed {
			t.Errorf("hcd %d: Speed %d, want %d", tt.hcd, hw.Speed(), tt.speed)
		}
	}

	// an unresolved HCD is an error, not a silent zero
	m := newModel()
	m.anegAfter = 0
	m.auxstat = 0
	hw := newPHY(t, m, RGMIIID)

	if _, err := hw.Status(); err == nil {
		t.Fatal("Status accepted an unresolved link")
	}
}
