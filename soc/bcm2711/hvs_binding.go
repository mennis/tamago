// BCM2711 HVS5 (SCALER5) register binding
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"fmt"
	"sync"

	"github.com/usbarmory/tamago/soc/bcm2711/hvs"
)

// HVS register offsets, relative to HVSOffset. Broadcom documents none of this
// block; the names and offsets are the Linux vc4 driver's (SCALER_* in
// vc4_regs.h) and each one was matched against a register dump taken from this
// board with a live display, which is what confirms the base is right.
const (
	SCALER_DISPCTRL  = 0x0000
	SCALER_DISPSTAT  = 0x0004
	SCALER_DISPID    = 0x0008
	SCALER_DISPEOLN  = 0x0018
	SCALER_DISPLIST0 = 0x0020

	// Per-channel block at stride 0x10. DISPBASE and DISPLACT are deliberately
	// absent: they were exported, never referenced and never validated, which
	// is the shape of the two constants that have already been wrong here. An
	// exported constant nobody reads implies a check nobody performed.
	//
	// DISPBKGND is kept without a reader because a background colour register
	// is the next thing an ARM side mode set needs. Whoever adds that reader
	// should note that its bit 31 means one thing on HVS4 and another here.
	//
	// The offsets themselves are anchored by DISPID reading a fixed signature,
	// and by the three control registers at stride 0x10 reading back as one
	// live channel and two idle ones. A wrong stride would not produce
	// either.
	SCALER_DISPCTRL0  = 0x0040
	SCALER_DISPBKGND0 = 0x0044
	SCALER_DISPSTAT0  = 0x0048

	SCALER_CHANNEL_STRIDE = 0x10

	// Where display list RAM begins inside the block. DISPLIST0 holds a word
	// index into this RAM, not a byte offset.
	//
	// This is the SCALER5 form. The header defines both, four characters apart,
	// and they differ by a factor of two. The HVS4 one was used here first, and
	// what it produced is worse than a bad read: the scratch region lands
	// inside the register window, so a display list written there would have
	// gone into HVS registers, and the read-back that looked like RAM was the
	// block's own signature aliased into it.
	//
	// At the correct base the RAM is the upper half of the block the device
	// tree gives the HVS, which also confirms the size constant is bytes.
	SCALER_DLIST_START = 0x4000
)

// SCALER_DISPCTRL bits.
const (
	SCALER_DISPCTRL_ENABLE = 1 << 31
)

// SCALER_DISPCTRLX_ENABLE is kept here because the binding reports it; the
// WIDTH and HEIGHT field arithmetic lives in package hvs (DecodeDispCtrl), where
// a test can reach it. See the comment there for why that move happened.
const SCALER_DISPCTRLX_ENABLE = 1 << 31

// HVSChannels is the number of display channels (FIFOs) the HVS drives. On this
// SoC channel 0 is the one the firmware brings up for HDMI0.
const HVSChannels = 3

// dlistWords is how many words of display-list RAM this block has.
//
// vc4_regs.h gives SCALER_DLIST_SIZE as 0x4000 without saying whether it counts
// bytes or words, and the two readings differ by 4x. The device tree settles it,
// and more cleanly than the first attempt at this argument did: the HVS is 0x8000
// long, display-list RAM starts at 0x4000 (see SCALER_DLIST_START), so 0x4000
// BYTES fills exactly the upper half of the block and 0x4000 words would run four
// times past its end.
//
// So this is the true size, 4096 words, and not merely a safe underestimate.
const dlistWords = 0x4000 / 4

// scratchDlist is the word index this driver writes its own display list to.
//
// Chosen far away from the firmware's, whose lists and Linux's allocator both
// sit in the low couple of hundred words. A quarter of the way into the RAM is
// above anything the firmware uses and well below the end.
//
// Writing here is inert: display list RAM is just memory until DISPLIST0 points
// at it, which is what makes the takeover in [SetPlane] reversible by a single
// register write.
const scratchDlist = 1024

// firmwareDlist remembers each channel's ORIGINAL display-list index, latched
// the first time SetPlane takes that channel over.
//
// Guarded by its own mutex: both writers do a latch plus a hardware
// read-modify-write, and the race between them is invisible to the race
// detector because this package cannot be built off-target.
//
// Without the latch the takeover is reversible exactly once, as a second call
// would read back our own list index and record that as the value to restore.
// The bug needs two calls to appear, so it survives any test that takes over
// once.
var firmwareDlist struct {
	sync.Mutex

	ch [HVSChannels]struct {
		word  uint32
		known bool
	}
}

// HVSRead reads an HVS register.
func HVSRead(offset uint64) uint32 {
	return Read32(PeripheralAddress(HVSOffset + offset))
}

// HVSWrite writes an HVS register.
func HVSWrite(offset uint64, val uint32) {
	Write32(PeripheralAddress(HVSOffset+offset), val)
}

// dlistAddr returns the register offset of a display-list word.
func dlistAddr(word uint32) uint64 {
	return SCALER_DLIST_START + uint64(word)*4
}

// HVSEnabled reports whether the HVS as a whole is running.
func HVSEnabled() bool {
	return HVSRead(SCALER_DISPCTRL)&SCALER_DISPCTRL_ENABLE != 0
}

// HVSChannel describes one display channel's configuration as the hardware
// currently holds it.
type HVSChannel struct {
	// Enabled reports the channel's own enable bit, which is separate from
	// the HVS-wide one.
	Enabled bool

	// Width and Height are the channel's output size in pixels — the mode the
	// pixelvalve downstream is timing. This is the authoritative answer to
	// "what resolution is actually being scanned out", independent of anything
	// the firmware reports through the mailbox.
	Width, Height uint32

	// DlistStart is the word index in display-list RAM where this channel's
	// list begins.
	DlistStart uint32

	// Raw is the unmodified DISPCTRLX value, kept so a caller reporting an
	// unexpected configuration can show what it actually read.
	Raw uint32
}

// Channel returns the configuration of an HVS display channel.
func Channel(n int) (c HVSChannel, err error) {
	if n < 0 || n >= HVSChannels {
		return c, fmt.Errorf("bcm2711: HVS channel %d out of range", n)
	}

	off := uint64(SCALER_DISPCTRL0 + n*SCALER_CHANNEL_STRIDE)

	c.Raw = HVSRead(off)
	c.Enabled, c.Width, c.Height = hvs.DecodeDispCtrl(c.Raw)
	c.DlistStart = HVSRead(uint64(SCALER_DISPLIST0 + n*4))

	return
}

// EnabledChannel returns the lowest-numbered HVS channel that is enabled, or an
// error if none is.
//
// Which channel drives the panel is not a constant: measured on one board
// within a day, Linux drove the same output through channel 0 and the firmware
// alone through channel 1. Code that hardcodes what it saw under Linux finds a
// disabled channel on a bare-metal boot, or reconfigures a channel nothing is
// scanning.
func EnabledChannel() (n int, c HVSChannel, err error) {
	for n = 0; n < HVSChannels; n++ {
		if c, err = Channel(n); err != nil {
			return 0, c, err
		}

		if c.Enabled {
			return n, c, nil
		}
	}

	return 0, HVSChannel{}, fmt.Errorf("bcm2711: no HVS channel is enabled")
}

// VerifyDisplayListRAM checks that display-list RAM is where this driver thinks
// it is, by writing a pattern to the scratch region and reading it back.
//
// Worth doing before trusting a display list read out of hardware. A wrong base
// does not fault, it reads elsewhere in the register window and returns
// plausible words. The base was wrong once, and the first snapshot read nine
// identical words from what it believed was the firmware's list.
//
// It probes before it writes, because the write is inert only if the base is
// right, which is the one thing this exists to doubt. Restoring a register by
// writing back what was read does not undo a write-1-to-clear bit, nor undo
// whatever the block did while its control register briefly held a test
// pattern. The probe is decisive: DISPID reads a fixed signature, so if the
// supposed RAM returns it, nothing should be written anywhere.
func VerifyDisplayListRAM() error {
	const pattern = 0xa5c3f00d

	// The signature the block reports at SCALER_DISPID. Seeing it where RAM is
	// expected is how the wrong base was caught the first time.
	const dispID = 0x64647276

	if got := HVSRead(dlistAddr(scratchDlist)); got == dispID {
		return fmt.Errorf("bcm2711: word %d of display-list RAM reads %#08x, which is"+
			" SCALER_DISPID — SCALER_DLIST_START is wrong and points into the"+
			" register window; refusing to write", scratchDlist, got)
	}

	saved := make([]uint32, 4)

	for i := range saved {
		saved[i] = HVSRead(dlistAddr(scratchDlist + uint32(i)))
	}

	for i := range saved {
		HVSWrite(dlistAddr(scratchDlist+uint32(i)), pattern+uint32(i))
	}

	var bad error

	for i := range saved {
		if got := HVSRead(dlistAddr(scratchDlist + uint32(i))); got != pattern+uint32(i) {
			bad = fmt.Errorf("bcm2711: display-list RAM did not round-trip at word %d:"+
				" wrote %#08x, read %#08x — SCALER_DLIST_START is probably wrong",
				scratchDlist+uint32(i), pattern+uint32(i), got)
			break
		}
	}

	for i := range saved {
		HVSWrite(dlistAddr(scratchDlist+uint32(i)), saved[i])
	}

	return bad
}

// ReadDisplayList reads n words of a channel's display list, starting at the
// word index the channel's DISPLIST register currently points at.
//
// This is how the firmware's own list is inspected before anything replaces it,
// and it is worth doing first on any board: the list says what the scanout is
// really pointed at, which is the one fact a mailbox cannot be asked for.
func ReadDisplayList(n int, words int) (list []uint32, err error) {
	c, err := Channel(n)

	if err != nil {
		return nil, err
	}

	if words <= 0 || c.DlistStart >= dlistWords || uint32(words) > dlistWords-c.DlistStart {
		return nil, fmt.Errorf("bcm2711: display list %d words at %d is outside the %d-word RAM",
			words, c.DlistStart, dlistWords)
	}

	list = make([]uint32, words)

	for i := range list {
		list[i] = HVSRead(dlistAddr(c.DlistStart + uint32(i)))
	}

	return
}

// WriteDisplayList writes words into display-list RAM at the given word index
// WITHOUT pointing any channel at them. Inert on its own.
func WriteDisplayList(start uint32, list []uint32) error {
	if len(list) == 0 {
		return fmt.Errorf("bcm2711: empty display list")
	}

	if start >= dlistWords || uint32(len(list)) > dlistWords-start {
		return fmt.Errorf("bcm2711: display list of %d words at %d overruns the %d-word RAM",
			len(list), start, dlistWords)
	}

	for i, w := range list {
		HVSWrite(dlistAddr(start+uint32(i)), w)
	}

	return nil
}

// SetDisplayList points a channel at a display list already written to RAM,
// returning the word index it was previously pointed at.
//
// The hardware latches the new list at the next frame boundary; there is no
// commit register on this generation. The returned value is what restores the
// previous scanout, and callers taking over from the firmware should keep it.
func SetDisplayList(n int, start uint32) (previous uint32, err error) {
	if n < 0 || n >= HVSChannels {
		return 0, fmt.Errorf("bcm2711: HVS channel %d out of range", n)
	}

	if start >= dlistWords {
		return 0, fmt.Errorf("bcm2711: display list start %d outside the %d-word RAM",
			start, dlistWords)
	}

	off := uint64(SCALER_DISPLIST0 + n*4)
	previous = HVSRead(off)
	HVSWrite(off, start)

	return
}

// SetPlane points an HVS channel at a single full-screen plane of the caller's
// own, taking the scanout over from whatever the firmware had it showing.
//
// It is the safest step of an ARM side mode set: the pixel clock, the HDMI PHY
// and the pixel valve timing stay as the firmware left them, and only which
// memory is scanned out changes.
//
// The takeover is reversible by construction. The new list goes to a scratch
// region of display-list RAM that the firmware's lists do not occupy, so the
// original is still intact when DISPLIST is switched; restoring it means writing
// the returned word index back, which SetDisplayList will do.
//
// The plane's Addr must be a physical address the VideoCore can reach — the low
// gigabyte, see GPUBusLimit — because the HVS masters memory as a VideoCore
// device and the display list carries the bus alias, not the ARM view.
//
// Two things this does NOT do, both deliberate. It does not enable the channel:
// a disabled channel is not scanning out and pointing it somewhere new will not
// change that, so the caller is told rather than having its request silently do
// nothing. And it does not check that the plane matches the channel's mode — a
// smaller plane on a larger channel is a legitimate thing to want, and the
// background shows through around it.
func SetPlane(n int, p *hvs.Plane) (previous uint32, err error) {
	// Locked for the whole function, not just the write.
	//
	// The overlap loop below READS firmwareDlist to tell our own scratch list
	// from a firmware one, and the latch at the end writes it. Taking the lock
	// only around the write left an unlocked read racing a locked write, and a
	// check-then-act window between them. Channel() is MMIO-only, so holding
	// across the loop costs nothing.
	firmwareDlist.Lock()
	defer firmwareDlist.Unlock()

	c, err := Channel(n)

	if err != nil {
		return 0, err
	}

	if !c.Enabled {
		return 0, fmt.Errorf("bcm2711: HVS channel %d is not enabled (DISPCTRL%d %#08x);"+
			" pointing it at a new list would not display anything", n, n, c.Raw)
	}

	// Geometry first, before the address arithmetic below depends on it. Every
	// field in a display-list element is written by masking, so an
	// out-of-range value silently becomes a plausible in-range one — and the
	// extent check that follows would then be computed from a Height the
	// hardware is not going to use. See [hvs.Plane.Validate].
	if err = p.Validate(); err != nil {
		return 0, err
	}

	if uint64(p.Addr)+uint64(p.Pitch)*uint64(p.Height) > GPUBusLimit {
		return 0, fmt.Errorf("bcm2711: plane at %#x+%#x is outside the GPU-addressable low 1GB",
			p.Addr, uint64(p.Pitch)*uint64(p.Height))
	}

	// The scratch region is only "clear of the firmware's lists" if that is
	// actually checked. Every enabled channel is examined, not just the target:
	// overwriting a list some OTHER channel is walking corrupts a display this
	// call never mentioned, and the symptom appears somewhere else entirely.
	for i := 0; i < HVSChannels; i++ {
		other, err := Channel(i)

		if err != nil {
			return 0, err
		}

		if !other.Enabled || (i == n && firmwareDlist.ch[n].known) {
			continue
		}

		if other.DlistStart < scratchDlist+hvs.ListWords &&
			other.DlistStart+hvs.ListWords > scratchDlist {
			// Distinguish OUR list from a firmware one, because the two need
			// opposite responses and the message decides which the reader
			// goes looking for. A channel sitting on scratchDlist that we
			// latched is a channel this driver already took over.
			if firmwareDlist.ch[i].known {
				return 0, fmt.Errorf("bcm2711: channel %d is already taken over;"+
					" there is one scratch region, so only one channel can be"+
					" driven from it at a time — release channel %d first", i, i)
			}

			return 0, fmt.Errorf("bcm2711: channel %d's display list at word %d overlaps"+
				" the scratch region [%d, %d); refusing to overwrite it",
				i, other.DlistStart, scratchDlist, scratchDlist+hvs.ListWords)
		}
	}

	if err = WriteDisplayList(scratchDlist, p.Element()); err != nil {
		return 0, err
	}

	// Latch the firmware's index on the FIRST takeover only. On any later call
	// DISPLIST already holds scratchDlist, and returning that would hand the
	// caller our own list as the thing to restore.
	if !firmwareDlist.ch[n].known {
		firmwareDlist.ch[n].word, firmwareDlist.ch[n].known = c.DlistStart, true
	}

	if _, err = SetDisplayList(n, scratchDlist); err != nil {
		return 0, err
	}

	return firmwareDlist.ch[n].word, nil
}

// ReleaseChannel restores a channel to the display list the firmware had it on,
// and forgets the takeover.
//
// Preferred over passing SetPlane's return value back to SetDisplayList, because
// it cannot be given the wrong index — the correct one is the one this package
// latched, and a caller that has taken over twice no longer has it.
func ReleaseChannel(n int) error {
	if n < 0 || n >= HVSChannels {
		return fmt.Errorf("bcm2711: HVS channel %d out of range", n)
	}

	firmwareDlist.Lock()
	defer firmwareDlist.Unlock()

	if !firmwareDlist.ch[n].known {
		return fmt.Errorf("bcm2711: channel %d was never taken over", n)
	}

	if _, err := SetDisplayList(n, firmwareDlist.ch[n].word); err != nil {
		return err
	}

	firmwareDlist.ch[n].known = false

	return nil
}
