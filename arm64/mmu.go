// ARM64 processor support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package arm64

import (
	"runtime"

	"github.com/usbarmory/tamago/arm64/mmucheck"
	"github.com/usbarmory/tamago/internal/reg"
)

// Page table offsets, relative to the L1 table base rather than to ramStart,
// so that a platform relocating the tables does not push every table past the
// space it reserved for them.
const (
	l1pageTableOffset    = 0x0000
	l2pageTableOffset    = 0x1000
	l3pageTableOffset    = 0x2000
	pageTableArenaOffset = 0x3000

	// defaultPageTableOffset places the L1 table when a platform does not
	// relocate it, reproducing the historical ramStart + 0x4000 layout.
	defaultPageTableOffset = 0x4000

	entriesPerTable = 512
	pageTableSize   = entriesPerTable * 8
)

// Platform configuration, set by SoC packages through go:linkname before
// [CPU.InitMMU] runs. Every value left zero yields the default behaviour.
var (
	// pageTableStart is the L1 table address, zero selects
	// ramStart + defaultPageTableOffset. A platform needs it when firmware
	// loads the image at ramStart, leaving no room below it for the tables.
	pageTableStart uint64

	// pageTableLimit is the first address past the space reserved for page
	// tables, the arena allocator refuses to cross it. Publish the usable
	// end, a guard page above the tables is not the allocator's to hand out.
	// Optional: see [mmucheck.ArenaEnd] for the bound derived without it.
	pageTableLimit uint64

	// lowMemStart and lowMemEnd describe an optional low memory scratch
	// window, mapped normal cacheable and execute never.
	lowMemStart uint64
	lowMemEnd   uint64

	// reservedMemStart and reservedMemEnd describe an optional window outside
	// the runtime RAM region which must nonetheless be mapped normal
	// cacheable and execute never, such as a firmware framebuffer carve out.
	// Without it such memory is mapped as Device, whose alignment rules fault
	// ordinary stores.
	reservedMemStart uint64
	reservedMemEnd   uint64

	// dmaMemStart and dmaMemEnd describe an optional window shared with bus
	// mastering devices, mapped Normal Non-cacheable and execute never, where
	// the CPU and the device observe each other without cache maintenance.
	//
	// A packed descriptor ring requires it, as several descriptors share a
	// cache line and cleaning one writes stale copies back over the
	// neighbours a device has just updated.
	//
	// This window takes precedence over every other classification, so it may
	// be carved out of the scratch window or of the RAM region.
	dmaMemStart uint64
	dmaMemEnd   uint64
)

type mmuMap struct {
	ramStart  uint64
	ramEnd    uint64
	textStart uint64
	textEnd   uint64

	// base is the L1 table address; the other fixed tables sit at the offsets
	// above it.
	base uint64

	arenaNext uint64
	arenaEnd  uint64
}

type mappingAction uint8

const (
	mappingDevice mappingAction = iota
	mappingMemoryExec
	mappingMemoryXN
	mappingNonCacheable
	mappingSplit
)

func alignDown(addr uint64, align uint64) uint64 {
	return addr &^ (align - 1)
}

func alignUp(addr uint64, align uint64) uint64 {
	return alignDown(addr+align-1, align)
}

func (m *mmuMap) init() {
	ramStart, ramEnd := runtime.MemRegion()
	textStart, textEnd := runtime.TextRegion()

	textStart = alignDown(textStart, pageTableSize)
	textEnd = alignUp(textEnd, pageTableSize)

	// Every window bound must be a whole page. L3 is the finest level, so a
	// boundary inside a 4KB page cannot be expressed at all and classifyPage
	// would panic partway through the walk with no idea which window caused
	// it. Checked here so the message names the window.
	if err := mmucheck.ValidateWindows(
		dmaMemStart, dmaMemEnd,
		reservedMemStart, reservedMemEnd,
		lowMemStart, lowMemEnd,
	); err != nil {
		panic("arm64: " + err.Error())
	}

	base := ramStart + defaultPageTableOffset
	arenaEnd := textStart

	if pageTableStart != 0 {
		base = pageTableStart
		arenaEnd = mmucheck.ArenaEnd(pageTableStart, pageTableLimit,
			lowMemStart, lowMemEnd, textStart)
	}

	arenaNext := base + pageTableArenaOffset

	// Nothing is committed to m until the layout is known good, so the checks
	// above and below are the same ones a platform can run on the host.
	if err := mmucheck.ValidateLayout(
		ramStart, ramEnd, textStart, textEnd, base, arenaNext, arenaEnd,
	); err != nil {
		panic("arm64: " + err.Error())
	}

	if err := mmucheck.ValidatePageTables(
		dmaMemStart, dmaMemEnd, base, arenaEnd,
	); err != nil {
		panic("arm64: " + err.Error())
	}

	m.ramStart = ramStart
	m.ramEnd = ramEnd
	m.textStart = textStart
	m.textEnd = textEnd
	m.base = base
	m.arenaEnd = arenaEnd
	m.arenaNext = arenaNext

	return
}

func (m *mmuMap) alloc() (addr uint64) {
	addr = m.arenaNext

	if addr+pageTableSize > m.arenaEnd {
		panic("out of early page table space; enlarge the platform's page table reservation")
	}

	m.arenaNext += pageTableSize

	return
}

// windowFor reports whether [addr,end) lies wholly inside the window
// [start,wend), and whether it overlaps it at all. A window with a zero end is
// disabled.
func windowFor(addr, end, start, wend uint64) (inside, overlaps bool) {
	if wend == 0 {
		return false, false
	}

	return addr >= start && end <= wend, addr < wend && end > start
}

// classifyBlock decides how one block of address space is mapped, or that it
// must be refined to a finer table.
//
// The DMA-coherent window is tested first as it may be carved out of any
// other, a cacheable case matching before it would map it cacheable. Every
// window contributes two cases, wholly inside maps and partially overlapping
// splits.
func (m *mmuMap) classifyBlock(addr uint64, end uint64) (action mappingAction) {
	if inside, overlaps := windowFor(addr, end, dmaMemStart, dmaMemEnd); inside {
		return mappingNonCacheable
	} else if overlaps {
		return mappingSplit
	}

	switch {
	case addr >= m.ramStart && end <= m.textEnd:
		// exception vector table
		action = mappingMemoryExec
	case addr >= m.textEnd && end <= m.ramEnd:
		// runtime memory
		action = mappingMemoryXN
	case addr >= m.ramStart && end <= m.ramEnd:
		// text/runtime boundary cross
		action = mappingSplit
	case addr < m.ramEnd && end > m.ramStart:
		// RAM boundary cross
		action = mappingSplit
	default:
		action = mappingDevice
	}

	if action != mappingDevice {
		return
	}

	// Outside RAM. The firmware-reserved and low-memory scratch windows both
	// live here, and both want ordinary memory semantics rather than the
	// Device mapping everything else outside RAM gets: Device-nGnRnE faults on
	// the unaligned stores ordinary Go code emits.
	for _, w := range [...][2]uint64{
		{reservedMemStart, reservedMemEnd},
		{lowMemStart, lowMemEnd},
	} {
		if inside, overlaps := windowFor(addr, end, w[0], w[1]); inside {
			return mappingMemoryXN
		} else if overlaps {
			return mappingSplit
		}
	}

	return
}

func (m *mmuMap) classifyPage(addr uint64) (action mappingAction) {
	end := addr + pageTableSize
	action = m.classifyBlock(addr, end)

	if action == mappingSplit {
		// Unreachable while every window and both RAM bounds are page
		// aligned, which newMMUMap and ValidateWindows check. L3 is the
		// finest level, so there is nothing left to split into.
		panic("MMU boundary is not 4KB aligned")
	}

	return
}

func writeMapping(page uint64, addr uint64, memoryRegion uint64, deviceRegion uint64, nonCacheableRegion uint64, action mappingAction) {
	switch action {
	case mappingMemoryExec:
		reg.Write64(page, addr|memoryRegion)
	case mappingMemoryXN:
		reg.Write64(page, addr|memoryRegion|TTE_EXECUTE_NEVER)
	case mappingNonCacheable:
		reg.Write64(page, addr|nonCacheableRegion|TTE_EXECUTE_NEVER)
	case mappingDevice:
		reg.Write64(page, addr|deviceRegion|TTE_EXECUTE_NEVER)
	default:
		panic("invalid MMU mapping action")
	}
}

// Memory region attributes
//
// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// G5.7.
const (
	TTE_XN   = 53
	TTE_AF   = 10
	TTE_SH   = 8
	TTE_AP   = 6
	TTE_ATTR = 2
	TTE_DESC = 0

	TTE_BLOCK         uint64 = (0b01 << TTE_DESC)
	TTE_TABLE         uint64 = (0b11 << TTE_DESC)
	TTE_PAGE          uint64 = (0b11 << TTE_DESC)
	TTE_NON_SH        uint64 = (0b00 << TTE_SH)
	TTE_OUTER_SH      uint64 = (0b10 << TTE_SH)
	TTE_INNER_SH      uint64 = (0b11 << TTE_SH)
	TTE_EXECUTE_NEVER uint64 = (0b11 << TTE_XN)

	// Device-nGnRnE
	DeviceRegion uint64 = 0b00000000
	// Normal, Inner/Outer WB/WA/RA
	MemoryRegion uint64 = 0b11111111
	// Normal, Inner and Outer Non-cacheable: ordinary memory semantics with
	// no cache allocation, so that CPU and device observe the same bytes
	// without maintenance. Device memory cannot serve, its alignment rules
	// fault on the unaligned stores ordinary Go code emits.
	NonCacheableRegion uint64 = 0b01000100

	deviceAttributeIndex       = 0
	memoryAttributeIndex       = 1
	nonCacheableAttributeIndex = 2

	deviceAttributes = 1<<TTE_AF | TTE_OUTER_SH | TTE_AP_00<<TTE_AP | deviceAttributeIndex<<TTE_ATTR
	memoryAttributes = 1<<TTE_AF | TTE_INNER_SH | TTE_AP_00<<TTE_AP | memoryAttributeIndex<<TTE_ATTR
	// Normal Non-cacheable is architecturally treated as Outer Shareable
	// regardless of the descriptor's shareability field (ARM ARM B2.7.2).
	nonCacheableAttributes = 1<<TTE_AF | TTE_OUTER_SH | TTE_AP_00<<TTE_AP | nonCacheableAttributeIndex<<TTE_ATTR
)

// MMU access permissions
//
// ARM Architecture Reference Manual ARMv8,for ARMv8-A architecture profile
// Table D5-25, Data access permissions for stage 1 translations.
const (
	// EL1 or above: read/write, EL0: none
	TTE_AP_00 = 0b00
	// EL1 or above: read/write, EL0: read/write
	TTE_AP_01 = 0b01
	// EL1 or above: read-only, EL0: none
	TTE_AP_10 = 0b10
	// EL1 or above: read-only, EL0: read-only
	TTE_AP_11 = 0b11
)

// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D12.2.103 TCR_EL1, Translation Control Register (EL1).
const (
	TCR_IPS   = 32
	TCR_TBID  = 29
	TCR_EPD1  = 23
	TCR_TG0   = 14
	TCR_SH0   = 12
	TCR_ORGN0 = 10
	TCR_IRGN0 = 8
	TCR_T0SZ  = 0

	// 32-bit or 40-bit intermediate physical address size
	tcr uint64 = 0b010<<TCR_IPS |
		// disable TTBR1_EL1 translation table walks
		0b1<<TCR_EPD1 |
		// 4KB granule
		0b00<<TCR_TG0 |
		// inner shareable
		0b11<<TCR_SH0 |
		// outer cacheability (normal, cacheable)
		0b01<<TCR_ORGN0 |
		// inner cacheability (normal, cacheable)
		0b01<<TCR_IRGN0 |
		// memory region size offset 0:5 (39-bits VA space)
		25<<TCR_T0SZ
)

// defined in mmu.s
func flush_tlb()
func write_mair_el1(val uint64)
func write_tcr_el1(val uint64)
func set_ttbr0_el1(addr uint64)

// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D5.3.1 Translation table level 0, level 1, and level 2 descriptor formats.
func (m *mmuMap) initL1Table(entry int, ttbr uint64, section uint64) {
	n := 30 // 1GB

	memoryRegion := memoryAttributes | TTE_BLOCK
	deviceRegion := deviceAttributes | TTE_BLOCK
	nonCacheableRegion := nonCacheableAttributes | TTE_BLOCK

	for i := uint64(entry); i < entriesPerTable; i++ {
		page := ttbr + 8*i
		addr := section + (i << n)
		end := addr + (1 << n)
		action := m.classifyBlock(addr, end)

		if action == mappingSplit {
			next := m.alloc()
			reg.Write64(page, next|TTE_TABLE)
			m.initL2Table(0, next, addr)
		} else {
			writeMapping(page, addr, memoryRegion, deviceRegion, nonCacheableRegion, action)
		}
	}
}

// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D5.3.1 Translation table level 0, level 1, and level 2 descriptor formats.
func (m *mmuMap) initL2Table(entry int, base uint64, section uint64) {
	n := 21 // 2MB

	memoryRegion := memoryAttributes | TTE_BLOCK
	deviceRegion := deviceAttributes | TTE_BLOCK
	nonCacheableRegion := nonCacheableAttributes | TTE_BLOCK

	for i := uint64(entry); i < entriesPerTable; i++ {
		page := base + 8*i
		addr := section + (i << n)
		end := addr + (1 << n)
		action := m.classifyBlock(addr, end)

		if action == mappingSplit {
			next := m.alloc()
			reg.Write64(page, next|TTE_TABLE)
			m.initL3Table(0, next, addr)
		} else {
			writeMapping(page, addr, memoryRegion, deviceRegion, nonCacheableRegion, action)
		}
	}
}

// ARM Architecture Reference Manual ARMv8, for ARMv8-A architecture profile
// D5.3.2 ARMv8 translation table level 3 descriptor formats.
func (m *mmuMap) initL3Table(entry int, base uint64, section uint64) {
	n := 12 // 4KB

	memoryRegion := memoryAttributes | TTE_PAGE
	deviceRegion := deviceAttributes | TTE_PAGE
	nonCacheableRegion := nonCacheableAttributes | TTE_PAGE

	for i := uint64(entry); i < entriesPerTable; i++ {
		page := base + 8*i
		addr := section + (i << n)

		writeMapping(page, addr, memoryRegion, deviceRegion, nonCacheableRegion, m.classifyPage(addr))
	}
}

// InitMMU initializes translation tables for all available memory with a flat
// mapping.
//
// The first 4096 bytes (0x00000000 - 0x00001000) are flagged as invalid to
// trap null pointers.
//
// All available memory is marked as non-executable except for the range
// returned by runtime.TextRegion().
func (cpu *CPU) InitMMU() {
	m := &mmuMap{}
	m.init()

	l1pageTableStart := m.base + l1pageTableOffset
	l2pageTableStart := m.base + l2pageTableOffset
	l3pageTableStart := m.base + l3pageTableOffset

	// Map the first L1 entry to an L2 table.
	tte := l2pageTableStart | TTE_TABLE
	reg.Write64(l1pageTableStart, tte)

	// Map the first L2 entry to an L3 table to trap null pointers within
	// the smallest possible section (4KB starting from 0x00000000).
	tte = l3pageTableStart | TTE_TABLE
	reg.Write64(l2pageTableStart, tte)

	// set first L3 entry as invalid
	reg.Write64(l3pageTableStart, 0)

	// set remaining entries with flat mapping
	m.initL1Table(1, l1pageTableStart, 0)
	m.initL2Table(1, l2pageTableStart, 0)
	m.initL3Table(1, l3pageTableStart, 0)

	// set memory region attributes
	//   * attr0: device
	//   * attr1: memory
	//   * attr2: memory, non-cacheable (DMA-coherent windows)
	write_mair_el1(
		MemoryRegion<<(8*memoryAttributeIndex) |
			DeviceRegion<<(8*deviceAttributeIndex) |
			NonCacheableRegion<<(8*nonCacheableAttributeIndex))

	// set translation control register
	write_tcr_el1(tcr)

	// Discard stale data cache lines left by firmware, the boot ROM or a
	// previous OS before enabling the MMU turns the data cache on, they
	// would otherwise shadow memory this image has already written.
	dcache_invalidate_all()

	// enable MMU
	set_ttbr0_el1(l1pageTableStart)
}
