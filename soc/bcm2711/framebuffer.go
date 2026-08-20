// BCM2711 SoC VideoCore framebuffer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"encoding/binary"
	"fmt"
)

// FramebufferInfo describes a framebuffer allocated by the VideoCore firmware,
// see [AllocateFramebuffer]. Addr is the ARM physical address, with the GPU bus
// alias already stripped.
//
// The caller owns the mapping and it is not a detail. The firmware allocates
// out of its carve-out at the top of the low gigabyte, which a board package
// typically maps as Device because the rest of it is the VideoCore's. Device
// memory forbids unaligned accesses and does not merge byte stores, so a
// consumer drawing a byte at a time must first map the granted range normal
// cacheable; one that leaves it Device must confine itself to aligned 32-bit
// stores. This is a fault on real hardware, not a style note.
//
// Once mapped cacheable, CPU writes sit in the data cache where the scanout
// engine cannot see them and must be cleaned out before they appear.
type FramebufferInfo struct {
	Width         uint32 // pixels per row
	Height        uint32 // visible rows (the physical mode)
	VirtualHeight uint32 // virtual-surface rows, never less than Height
	Depth         uint32 // bits per pixel, DERIVED from the pitch (see AllocateFramebuffer)
	Pitch         uint32 // bytes per row (may exceed Width*Depth/8)
	Addr          uint64 // ARM physical address
	Size          uint32 // total bytes granted

	// PixelOrder is the channel order the firmware reports, 0 for BGR and 1
	// for RGB. It is recorded for diagnosis and deliberately not acted on.
	//
	// The report is wrong on this path, and there is now a witness. A Pi 4
	// Model B driving a 480x800 panel reported 0 (BGR) for a 32bpp surface,
	// while a test card written as xRGB -- red in bits 16-23 -- rendered with
	// every colour correct on the glass: the seven palette bars each matched
	// their own printed label, and the gopher came out orange rather than
	// cyan. Obeying the report would have swapped red and blue on an image
	// that was already right.
	//
	// So a caller wanting the panel's true channel order has to render
	// something known and look at it. That is what this field is for, and it
	// is why nothing in this package consumes it.
	PixelOrder uint32
}

// DisplayPhysicalSize returns the display dimensions as the VideoCore firmware
// currently sees them, or 0x0 when the query fails.
//
// Before anything has allocated a framebuffer the firmware answers with
// whatever its uninitialized display state holds, which need not be a mode any
// panel has. Treat an implausible answer as no display configured and ask for
// the geometry you want instead.
func DisplayPhysicalSize() (width, height uint32) {
	msg := &MailboxMessage{
		Tags: []MailboxTag{{ID: FB_GET_PHYSICAL_WH, Buffer: make([]byte, FB_GET_PHYSICAL_WH_LEN)}},
	}

	if err := Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return
	}

	tag := msg.Tag(FB_GET_PHYSICAL_WH)

	if tag == nil || len(tag.Buffer) < 8 {
		return
	}

	return binary.LittleEndian.Uint32(tag.Buffer), binary.LittleEndian.Uint32(tag.Buffer[4:])
}

// DisplayPhysicalSizeOr returns the firmware's display dimensions, or the given
// defaults when no display was detected — the single place a board package's
// fallback geometry needs to be spelled out.
func DisplayPhysicalSizeOr(defWidth, defHeight uint32) (width, height uint32) {
	width, height = DisplayPhysicalSize()

	if width == 0 || height == 0 {
		width, height = defWidth, defHeight
	}

	return
}

// AllocateFramebuffer requests a width x height framebuffer of depth bits per
// pixel from the VideoCore firmware, returning the grant.
//
// The mode set and the allocation travel as one property message, because some
// firmware latches the mode off the batched transaction and an allocation
// issued alone can be served against the previous mode. It also closes the
// window in which another mailbox user could reconfigure the display between
// the two halves of one logical operation.
//
// The allocation is honest on this SoC, but nothing here relies on that:
//
//   - SET echoes are ignored. A SET tag's response is the request, not the
//     applied state, on any firmware.
//   - GET_DEPTH is ignored. Depth is derived as Pitch*8/Width, from the two
//     values the firmware has never been caught lying about — the pitch and the
//     grant size, both of which its own code depends on.
//   - The derivation is cross-checked against the grant: Pitch*Height must fit
//     in Size, or the geometry is rejected rather than drawn into.
//
// The firmware may grant a different geometry than requested; the returned
// Width, Height and Pitch are what the caller must draw with, and depth is
// advisory in the same way.
func AllocateFramebuffer(width, height, depth uint32) (info *FramebufferInfo, err error) {
	phys := make([]byte, FB_SET_PHYSICAL_WH_LEN)
	binary.LittleEndian.PutUint32(phys, width)
	binary.LittleEndian.PutUint32(phys[4:], height)

	virt := make([]byte, FB_SET_VIRTUAL_WH_LEN)
	binary.LittleEndian.PutUint32(virt, width)
	binary.LittleEndian.PutUint32(virt[4:], height)

	dep := make([]byte, FB_SET_DEPTH_LEN)
	binary.LittleEndian.PutUint32(dep, depth)

	// The allocation tag carries the requested alignment on the way in and
	// (base, size) on the way out; FB_ALLOCATE_BUFFER_LEN is the reply's size,
	// which is the one that has to fit.
	alloc := make([]byte, FB_ALLOCATE_BUFFER_LEN)
	binary.LittleEndian.PutUint32(alloc, 4096)

	msg := &MailboxMessage{
		Tags: []MailboxTag{
			{ID: FB_SET_PHYSICAL_WH, Buffer: phys},
			{ID: FB_SET_VIRTUAL_WH, Buffer: virt},
			{ID: FB_SET_DEPTH, Buffer: dep},
			{ID: FB_ALLOCATE_BUFFER, Buffer: alloc},
			{ID: FB_GET_PHYSICAL_WH, Buffer: make([]byte, FB_GET_PHYSICAL_WH_LEN)},
			{ID: FB_GET_VIRTUAL_WH, Buffer: make([]byte, FB_GET_VIRTUAL_WH_LEN)},
			{ID: FB_GET_PITCH, Buffer: make([]byte, FB_GET_PITCH_LEN)},
			{ID: FB_GET_PIXEL_ORDER, Buffer: make([]byte, FB_GET_PIXEL_ORDER_LEN)},
		},
	}

	if err = Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return nil, fmt.Errorf("framebuffer property request failed: %v", err)
	}

	// Every field of the reply is read out before any further mailbox message
	// is sent. Call replaces the tags with the decoded response, so a follow-up
	// through the same message overwrites the reply being parsed and a good
	// allocation reads back as a failure.
	tag := msg.Tag(FB_ALLOCATE_BUFFER)

	if tag == nil || len(tag.Buffer) < 8 {
		return nil, fmt.Errorf("malformed framebuffer allocation response")
	}

	busAddr := binary.LittleEndian.Uint32(tag.Buffer)
	size := binary.LittleEndian.Uint32(tag.Buffer[4:])

	if busAddr == 0 || size == 0 {
		return nil, fmt.Errorf("firmware granted no framebuffer")
	}

	// Bits [31:30] of a bus address select a cache alias of the same physical
	// memory, and this driver understands only the uncached one.
	//
	// The alias is checked rather than masked off, as masking turns any of them
	// into a plausible looking physical address: a grant in a cached alias
	// would then be accepted and drawn into under a normal cacheable mapping,
	// whose symptom is stale or torn pixels rather than a fault.
	if busAddr&GPU_BUS_OFFSET != GPU_BUS_OFFSET {
		return nil, fmt.Errorf("framebuffer granted in an unexpected bus alias"+
			" (bus %#x, alias bits %#x, want %#x)",
			busAddr, busAddr&GPU_BUS_OFFSET, uint32(GPU_BUS_OFFSET))
	}

	info = &FramebufferInfo{
		Addr: uint64(busAddr &^ GPU_BUS_OFFSET),
		Size: size,
	}

	// The applied physical mode: both dimensions, so that Width and Height
	// always describe one consistent mode and the bpp division below divides
	// matching quantities.
	if tag = msg.Tag(FB_GET_PHYSICAL_WH); tag != nil && len(tag.Buffer) >= 8 {
		info.Width = binary.LittleEndian.Uint32(tag.Buffer)
		info.Height = binary.LittleEndian.Uint32(tag.Buffer[4:])
	}

	if tag = msg.Tag(FB_GET_VIRTUAL_WH); tag != nil && len(tag.Buffer) >= 8 {
		info.VirtualHeight = binary.LittleEndian.Uint32(tag.Buffer[4:])
	}

	if tag = msg.Tag(FB_GET_PITCH); tag != nil && len(tag.Buffer) >= 4 {
		info.Pitch = binary.LittleEndian.Uint32(tag.Buffer)
	}

	// Advisory only; see the field doc. A missing tag defaults to RGB (1),
	// which is what the packers produce.
	info.PixelOrder = 1
	if tag = msg.Tag(FB_GET_PIXEL_ORDER); tag != nil && len(tag.Buffer) >= 4 {
		info.PixelOrder = binary.LittleEndian.Uint32(tag.Buffer)
	}

	if info.Width == 0 || info.Height == 0 || info.Pitch == 0 {
		return nil, fmt.Errorf("firmware reported no applied geometry (%dx%d pitch %d)",
			info.Width, info.Height, info.Pitch)
	}

	// The virtual surface is never smaller than the visible mode. Flooring a
	// missing or nonsensical report to Height keeps that invariant without
	// inventing off-screen rows nothing has proven exist.
	if info.VirtualHeight < info.Height {
		info.VirtualHeight = info.Height
	}

	// Bytes per pixel from the pitch rather than from GET_DEPTH. The division
	// is exact whenever the row padding is smaller than Width bytes, which
	// holds for any real alignment. Either way it is bounds safe, as the row
	// and total fits are both checked, so a mis-derived depth is a visibly
	// wrong image rather than an out-of-bounds write.
	bpp := info.Pitch / info.Width
	if bpp < 1 || bpp > 4 {
		return nil, fmt.Errorf("implausible framebuffer pixel size (%dx%d pitch %d)",
			info.Width, info.Height, info.Pitch)
	}
	info.Depth = bpp * 8

	if uint64(info.Pitch)*uint64(info.Height) > uint64(info.Size) {
		return nil, fmt.Errorf("inconsistent framebuffer geometry (%dx%d@%d pitch %d size %d)",
			info.Width, info.Height, info.Depth, info.Pitch, info.Size)
	}

	// Fail closed on a grant running off the end of the window the GPU alias
	// covers. The carve-out is sized by gpu_mem and is not at a fixed address,
	// so this architectural bound is all there is to assert against.
	//
	// Note precisely what this can and cannot catch, because it reads stronger
	// than it is. Addr was produced by masking off bits [31:30], so it is below
	// GPUBusLimit BY CONSTRUCTION and no base address can ever fail here. Only
	// the addition can trip it — a buffer based inside the window whose length
	// carries it past the top. The out-of-window base this looks like it guards
	// is guarded instead by the alias check above, which is where a bus address
	// that does not belong to this window is actually rejected.
	if info.Addr+uint64(info.Size) > GPUBusLimit {
		return nil, fmt.Errorf("framebuffer %#x+%#x outside the GPU-addressable low 1GB",
			info.Addr, info.Size)
	}

	return info, nil
}

// SetVirtualOffset pans the scanout so the visible mode shows the virtual
// surface starting at pixel (x, y).
//
// A nil return means the firmware accepted the request, not that it panned:
// the response echoes the requested offset either way and this tag has no size
// witness to check it against (see AllocateFramebuffer on what is believed).
// The verdict is on the panel.
func SetVirtualOffset(x, y uint32) error {
	buf := make([]byte, FB_SET_VIRTUAL_OFFSET_LEN)
	binary.LittleEndian.PutUint32(buf, x)
	binary.LittleEndian.PutUint32(buf[4:], y)

	msg := &MailboxMessage{
		Tags: []MailboxTag{{ID: FB_SET_VIRTUAL_OFFSET, Buffer: buf}},
	}

	if err := Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return fmt.Errorf("virtual offset request rejected: %v", err)
	}

	return nil
}

// BlankScreen asks the firmware to blank (true) or unblank (false) the display.
// The tag is advisory in the firmware's own documentation — it may be a no-op
// on a given pipeline — so an error here means the request was refused, while
// success means only that it was accepted.
func BlankScreen(blank bool) error {
	buf := make([]byte, FB_BLANK_LEN)
	if blank {
		binary.LittleEndian.PutUint32(buf, 1)
	}

	msg := &MailboxMessage{
		Tags: []MailboxTag{{ID: FB_BLANK, Buffer: buf}},
	}

	if err := Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return fmt.Errorf("blank request rejected: %v", err)
	}

	return nil
}

// ReleaseFramebuffer releases the firmware-allocated framebuffer. It reports
// the firmware's refusal as an error rather than swallowing it, so a caller
// that re-allocates in a loop can notice a wedged firmware instead of leaking
// the carve-out one grant at a time.
//
// The buffer is the firmware's memory rather than the Go heap's: after this
// returns, anything still pointing at the grant points at memory the firmware
// may reuse.
func ReleaseFramebuffer() error {
	msg := &MailboxMessage{
		Tags: []MailboxTag{{ID: FB_RELEASE_BUFFER}},
	}

	if err := Mailbox.Call(CH_PROPERTYTAGS_A_TO_VC, msg); err != nil {
		return fmt.Errorf("framebuffer release rejected: %v", err)
	}

	return nil
}
