// BCM54213PE Ethernet PHY support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

// Package bcm54213 implements a driver for the Broadcom BCM54213PE Gigabit
// Ethernet Transceiver adopting the following specifications:
//   - 54210PE-DS10-R (01-24-14)
package bcm54213

import (
	"errors"
	"fmt"
	"time"

	"github.com/usbarmory/tamago/bits"
	"github.com/usbarmory/tamago/phy"
)

// PHY registers
const (
	BASIC_CONTROL     = 0x00
	CTRL_ANEG_RESTART = 9
	CTRL_ANEG_ENABLE  = 12

	BASIC_STATUS         = 0x01
	STATUS_LINK          = 2
	STATUS_ANEG_COMPLETE = 5

	PHY_ID_1 = 0x02
	PHY_ID_2 = 0x03

	// auxiliary control, shadow select [2:0] and read select [14:12]
	PHY_AUX_CONTROL  = 0x18
	AUX_MISC         = 0x7
	AUX_MISC_READ    = 0x7 << 12
	AUX_RGMII_SKEW   = 8
	AUX_WRITE_ENABLE = 15

	// auxiliary status, resolved highest common denominator in [10:8]
	PHY_AUX_STATUS = 0x19
	AUX_HCD        = 8

	// shadow access, select [14:10] and data [9:0]
	PHY_SHADOW       = 0x1c
	SHD_CLOCK_CTRL   = 0x3 << 10
	SHD_DATA         = 0x3ff
	SHD_GTXCLK_DELAY = 9
	SHD_WRITE        = 15
)

// PHY identifier, the revision nibble is masked off so that later steppings
// are accepted.
const (
	ID      = 0x600d84a0
	ID_MASK = 0xfffffff0
)

// A PHY held in reset, or a floating MDIO bus, reads all ones. That value
// carries every status bit this driver tests, so it is rejected explicitly
// rather than decoded.
const silent = 0xffff

// Reset pulse widths, the settle time covers the BCM54xx MDIO ready
// requirement after deassertion.
const (
	ResetAssert = 20 * time.Millisecond
	ResetSettle = 150 * time.Millisecond
)

// Timeout is the default timeout for PHY operations.
const Timeout = 100 * time.Millisecond

// Mode represents which side of the RGMII interface supplies the clock
// delays, following the Linux phy-mode naming.
type Mode int

const (
	// RGMII: the MAC or board supplies both delays.
	RGMII Mode = iota
	// RGMIIID: the PHY supplies both delays.
	RGMIIID
	// RGMIIRXID: the PHY supplies the RXC to RXD skew only.
	RGMIIRXID
	// RGMIITXID: the PHY supplies the GTXCLK delay only.
	RGMIITXID
)

func (m Mode) skew() bool  { return m == RGMIIID || m == RGMIIRXID }
func (m Mode) delay() bool { return m == RGMIIID || m == RGMIITXID }

// Status represents the resolved PHY link state.
type Status struct {
	Link                    bool
	AutoNegotiationComplete bool
	Speed                   int
	FullDuplex              bool
}

// PHY represents a BCM54213PE PHY port.
type PHY struct {
	// Timeout for PHY operations
	Timeout time.Duration

	// RGMII clock delay ownership the board is wired for
	Mode Mode

	// ResetLine drives the hardware reset line, true asserts reset. The
	// board owns the pin and its polarity.
	ResetLine func(assert bool)

	// Sleep overrides time.Sleep, for callers running before the scheduler.
	Sleep func(time.Duration)

	miim  phy.MIIM
	pa    int
	speed int
}

// Init initializes a BCM54213PE PHY instance for register access.
func (hw *PHY) Init(addr int, miim phy.MIIM) (err error) {
	if miim == nil || addr < 0 || addr > 31 {
		return errors.New("invalid PHY instance")
	}

	hw.miim = miim
	hw.pa = addr
	hw.speed = 0

	if hw.Timeout == 0 {
		hw.Timeout = Timeout
	}

	return
}

func (hw *PHY) sleep(d time.Duration) {
	if hw.Sleep != nil {
		hw.Sleep(d)
		return
	}

	time.Sleep(d)
}

func (hw *PHY) read(address int) (data uint16, err error) {
	if hw.miim == nil {
		return 0, errors.New("invalid PHY instance")
	}

	return hw.miim.ReadPHYRegister(hw.pa, address)
}

func (hw *PHY) write(address int, data uint16) (err error) {
	if hw.miim == nil {
		return errors.New("invalid PHY instance")
	}

	return hw.miim.WritePHYRegister(hw.pa, address, data)
}

// shadow selects one of the shadow banks behind address and reads it back.
// The selection persists, so it must be repeated before any further read.
func (hw *PHY) shadow(address int, sel uint16) (data uint16, err error) {
	if err = hw.write(address, sel); err != nil {
		return
	}

	return hw.read(address)
}

// Reset pulses the hardware reset line through [PHY.ResetLine] and waits for
// the PHY to accept management access.
func (hw *PHY) Reset() (err error) {
	if hw.ResetLine == nil {
		return errors.New("no reset line configured")
	}

	hw.ResetLine(true)
	hw.sleep(ResetAssert)
	hw.ResetLine(false)
	hw.sleep(ResetSettle)

	return
}

// Identifier returns the PHY identifier registers as one 32-bit value.
func (hw *PHY) Identifier() (id uint32, err error) {
	var high, low uint16

	if high, err = hw.read(PHY_ID_1); err != nil {
		return
	}

	if low, err = hw.read(PHY_ID_2); err != nil {
		return
	}

	id = uint32(high)<<16 | uint32(low)

	// Diagnose a held or absent PHY before the identifier is judged, an all
	// ones read is not a foreign part number.
	if high == silent || low == silent {
		return id, errors.New("PHY is silent, held in reset or floating MDIO bus")
	}

	if id&ID_MASK != ID {
		return id, fmt.Errorf("unexpected PHY identifier %#08x", id)
	}

	return
}

// SetClockDelay programs the PHY side RGMII clock delays for [PHY.Mode] and
// verifies that they took effect. The RXC to RXD skew lives in the auxiliary
// control MISC shadow, the GTXCLK delay in shadow 0x03.
//
// A board wired for RGMIIID requires this before any traffic, without both
// delays every Gigabit frame reaches the wire with an invalid FCS.
func (hw *PHY) SetClockDelay() (err error) {
	misc, err := hw.shadow(PHY_AUX_CONTROL, AUX_MISC|AUX_MISC_READ)

	if err != nil {
		return
	}

	if misc == silent {
		return errors.New("PHY is silent")
	}

	bits.SetTo16(&misc, AUX_RGMII_SKEW, hw.Mode.skew())
	bits.Set16(&misc, AUX_WRITE_ENABLE)

	if err = hw.write(PHY_AUX_CONTROL, AUX_MISC|misc); err != nil {
		return
	}

	clock, err := hw.shadow(PHY_SHADOW, SHD_CLOCK_CTRL)

	if err != nil {
		return
	}

	if clock == silent {
		return errors.New("PHY is silent")
	}

	clock &= SHD_DATA
	bits.SetTo16(&clock, SHD_GTXCLK_DELAY, hw.Mode.delay())
	bits.Set16(&clock, SHD_WRITE)

	if err = hw.write(PHY_SHADOW, SHD_CLOCK_CTRL|clock); err != nil {
		return
	}

	skew, delay, err := hw.ClockDelay()

	if err != nil {
		return
	}

	if skew != hw.Mode.skew() || delay != hw.Mode.delay() {
		return errors.New("RGMII clock delay configuration did not take effect")
	}

	return
}

// ClockDelay reports the PHY side RGMII clock delays without modifying them.
func (hw *PHY) ClockDelay() (skew bool, delay bool, err error) {
	misc, err := hw.shadow(PHY_AUX_CONTROL, AUX_MISC|AUX_MISC_READ)

	if err != nil {
		return
	}

	clock, err := hw.shadow(PHY_SHADOW, SHD_CLOCK_CTRL)

	if err != nil {
		return
	}

	if misc == silent || clock == silent {
		return false, false, errors.New("PHY is silent")
	}

	return bits.Get16(&misc, AUX_RGMII_SKEW), bits.Get16(&clock, SHD_GTXCLK_DELAY), nil
}

// Negotiate enables and restarts auto-negotiation.
func (hw *PHY) Negotiate() (err error) {
	hw.speed = 0

	return hw.write(BASIC_CONTROL, (1<<CTRL_ANEG_ENABLE)|(1<<CTRL_ANEG_RESTART))
}

func (hw *PHY) status() (basic uint16, err error) {
	// STATUS_LINK is latch-low, the first read clears stale link state.
	if _, err = hw.read(BASIC_STATUS); err != nil {
		return
	}

	if basic, err = hw.read(BASIC_STATUS); err != nil {
		return
	}

	if basic == silent {
		return 0, errors.New("PHY is silent")
	}

	return
}

// Link reports whether the PHY link is up. The status register is read twice
// because its link bit is latched low.
func (hw *PHY) Link() (up bool, err error) {
	var basic uint16

	if basic, err = hw.status(); err != nil {
		return
	}

	return bits.Get16(&basic, STATUS_LINK), nil
}

// Status returns link, auto-negotiation, speed, and duplex state. Speed and
// duplex are the auto-negotiation resolved values reported by the auxiliary
// status register.
func (hw *PHY) Status() (status Status, err error) {
	var basic, aux uint16

	if basic, err = hw.status(); err != nil {
		return
	}

	hw.speed = 0

	status.Link = bits.Get16(&basic, STATUS_LINK)
	status.AutoNegotiationComplete = bits.Get16(&basic, STATUS_ANEG_COMPLETE)

	if !status.Link || !status.AutoNegotiationComplete {
		return
	}

	if aux, err = hw.read(PHY_AUX_STATUS); err != nil {
		return
	}

	if aux == silent {
		return status, errors.New("PHY is silent")
	}

	switch bits.GetN16(&aux, AUX_HCD, 0b111) {
	case 7:
		status.Speed = 1000
		status.FullDuplex = true
	case 6:
		status.Speed = 1000
	case 5:
		status.Speed = 100
		status.FullDuplex = true
	case 4, 3:
		status.Speed = 100
	case 2:
		status.Speed = 10
		status.FullDuplex = true
	case 1:
		status.Speed = 10
	default:
		return status, errors.New("PHY has no resolved mode")
	}

	hw.speed = status.Speed

	return
}

// Address returns the PHY address passed at [PHY.Init].
func (hw *PHY) Address() int {
	return hw.pa
}

// Speed returns the last resolved PHY link speed.
func (hw *PHY) Speed() int {
	return hw.speed
}
