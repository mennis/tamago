// BCM2712 SoC SMP support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"runtime"
	"runtime/goos"
	"slices"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/usbarmory/tamago/arm64/psci"
	"github.com/usbarmory/tamago/internal/reg"
)

// BCM2712 low-memory scratch map, below ramStart (see mem.go). Regions must not
// overlap, which layout_check.go verifies at compile time.
//
//	range               base       size      purpose
//	[0x00000,0x01000)   0x00000    0x01000   null-pointer trap (invalid)
//	[0x01000,0x0c000)   0x01000    0x0b000   reserved low scratch
//	[0x0c000,0x10000)   0x0c000    0x04000   mailbox buffer (MAILBOX_REGION_BASE/SIZE)
//	[0x10000,0x20000)   0x10000    0x10000   secondary-core stacks (MaxCores*secondaryStackSize)
//	[0x20000,0x24000)   0x20000    0x04000   arm64 page tables (three fixed + one arena)
//	[0x24000,0x25000)   0x24000    0x01000   guard page
//	[0x25000,0x25048)   0x25000    0x00048   SMP task slots ((MaxCores-1)*taskSize)
//	[0x30000,0x70000)   0x30000    0x40000   DMA-coherent window (DMACoherentBase/Size, mem.go)
//
// The DMA-coherent window alone is mapped Normal Non-cacheable.
//
// The arm64 MMU places three fixed tables (L1, L2, L3) at pageTablesBase and
// allocates one more per block split by a window boundary, from an arena
// bounded by pageTableLimit. An exhausted arena panics instead of reaching the
// task slots, and the guard page above it covers an error in the bound.
// arm64/mmucheck verifies the arena fits this layout.
const (
	// secondaryStackBase is the base for the secondary-core bootstrap
	// stacks; core i uses the descending stack [base+i*size-size, base+i*size).
	secondaryStackBase = 0x10000
	secondaryStackSize = 0x4000

	// pageTablesBase is the arm64 MMU translation table base, published as
	// pageTableStart in mem.go.
	pageTablesBase = 0x20000
	// pageTablesArena is the page table allocation limit above
	// pageTablesBase: the three fixed tables and one arena table.
	pageTablesArena = 0x4000

	// pageTablesGuard is an unused page between the arena and taskBase.
	pageTablesGuard = 0x1000

	// pageTablesSize is the whole reserved region, keeping taskBase page
	// aligned.
	pageTablesSize = pageTablesArena + pageTablesGuard

	// taskBase is the base of the per-core SMP task slots, placed
	// immediately above the reserved page-table region.
	taskBase = pageTablesBase + pageTablesSize
	taskSize = 24
)

// BCM2712 MPIDR values are 0x000, 0x100, 0x200 and 0x300, the core ID is
// therefore MPIDR_EL1 Aff1 (bits 15:8).
const mpidrAff1Shift = 8

// MaxCores is the number of cores on the BCM2712.
const MaxCores = 4

// arrivalTimeout bounds how long InitSMP waits for started secondaries to
// reach their task wait loop, which takes microseconds when healthy.
const arrivalTimeout = 100 * time.Millisecond

// task represents a CPU task for a secondary core.
type task struct {
	sp uint64
	gp uint64
	pc uint64
}

// BSP EL1 vector table and MMU state, mirrored by secondaries before entering
// the scheduler.
var (
	vecTableAddr uint64
	mairEL1Addr  uint64
	tcrEL1Value  uint64
	ttbr0EL1Addr uint64
	activeCores  uint32
	nextTaskCore uint32

	// taskCores maps the k-th task to the secondary core that runs it,
	// listing only cores that arrived (index 0, the BSP, is unused).
	taskCores [MaxCores]uint32

	// smpStarted is set once InitSMP has issued CPU_ON.
	smpStarted bool
)

// Per-core bring-up state, written by each secondary in smp.s: coreArrived[i]
// is set when core i reaches its task wait loop with MMU and caches on,
// coreStarted[i] counts the tasks it jumped to. The BSP only clears them in
// InitSMP.
var (
	coreArrived [MaxCores]uint32
	coreStarted [MaxCores]uint32
)

// CoreArrived reports whether secondary core i has reached its task wait loop.
// Core 0, the BSP, always reports true.
func CoreArrived(i int) bool {
	if i == 0 {
		return true
	}
	if i < 0 || i >= MaxCores {
		return false
	}
	return atomic.LoadUint32(&coreArrived[i]) != 0
}

// TasksIssued returns how many tasks Task has handed out since InitSMP, one per
// M started on a secondary core; compared with [CoreStarted] it shows a task
// that was issued but not taken up.
func TasksIssued() uint32 {
	return atomic.LoadUint32(&nextTaskCore)
}

// CoreStarted returns how many tasks secondary core i has started. Each
// secondary is handed exactly one task, the M it runs for the rest of the
// program, so 1 means core i is running Go code.
func CoreStarted(i int) uint32 {
	if i <= 0 || i >= MaxCores {
		return 0
	}
	return atomic.LoadUint32(&coreStarted[i])
}

// defined in smp.s
func secondaryEntry()
func read_vbar_el1() uint64
func read_mair_el1() uint64
func read_tcr_el1() uint64
func read_ttbr0_el1() uint64

func taskAddress(coreID uint64) uint64 {
	return taskBase + coreID*taskSize
}

// ID returns the BCM2712 core identifier from MPIDR_EL1 Aff1.
func ID() uint64 {
	return (ARM.ID() >> mpidrAff1Shift) & 0xff
}

// InitSMP wakes secondary cores and configures the Go runtime for
// multicore operation.
//
// A positive argument caps the total number of cores, a negative
// argument initializes all available cores, 0 or 1 disables SMP.
//
// Only secondaries arriving at their task wait loop within arrivalTimeout are
// given to the runtime, others are reported on the console with their PSCI
// CPU_ON return code (ALREADY_ON and ON_PENDING are not failures).
//
// SMP is set up once, further calls after secondaries have been powered on
// return without effect.
func InitSMP(n int) {
	// A core running an M answers a second CPU_ON with ALREADY_ON and never
	// reports arrival again, and clearing the task slots under it would let
	// Task hand it a second M.
	if smpStarted {
		return
	}

	if n == 0 || n == 1 {
		atomic.StoreUint32(&activeCores, 0)
		goos.Task = nil
		return
	}

	total := MaxCores
	if n > 0 && n < total {
		total = n
	}

	// Store BSP's vector table address for secondary cores
	vecTableAddr = read_vbar_el1()
	mairEL1Addr = read_mair_el1()
	tcrEL1Value = read_tcr_el1()
	ttbr0EL1Addr = read_ttbr0_el1()

	// Secondaries read these with caches off, clean them to the point of
	// coherency before CPU_ON (CleanDataCacheRange ends with DSB SY).
	cleanU64(&vecTableAddr)
	cleanU64(&mairEL1Addr)
	cleanU64(&tcrEL1Value)
	cleanU64(&ttbr0EL1Addr)

	// No task can be handed out until the cores that arrived are known.
	atomic.StoreUint32(&activeCores, 0)
	atomic.StoreUint32(&nextTaskCore, 0)
	for i := range coreArrived {
		atomic.StoreUint32(&coreArrived[i], 0)
		atomic.StoreUint32(&coreStarted[i], 0)
	}

	var rcs [MaxCores]int64
	var started []int

	smpStarted = true

	// Clear per-core task slots before powering on secondaries.
	for i := 1; i < total; i++ {
		addr := taskAddress(uint64(i))
		reg.Write64(addr+0, 0)
		reg.Write64(addr+8, 0)
		reg.Write64(addr+16, 0)

		// clean the zeroed slot to the point of coherency, so the
		// secondary cannot see stale non-zero sp as a ready task
		ARM.CleanDataCacheRange(uintptr(addr), taskSize)

		rcs[i] = ARM.PSCICPUOn(uint64(i)<<mpidrAff1Shift, uint64(funcPC(secondaryEntry)), 0)

		if psci.Started(rcs[i]) {
			started = append(started, i)
		}
	}

	// nanotime rather than ARM.GetTime, which is constant on this SoC as the
	// generic timer multiplier is left zero.
	deadline := nanotime() + int64(arrivalTimeout)
	ready := psci.Await(started, CoreArrived, func() bool { return nanotime() > deadline })

	for i := 1; i < total; i++ {
		if !slices.Contains(ready, i) {
			print("bcm2712: core ", i, " not started (CPU_ON ", psci.String(rcs[i]), ", arrived=", CoreArrived(i), "), running without it\n")
		}
	}

	for k, c := range ready {
		taskCores[k+1] = uint32(c)
	}

	total = 1 + len(ready)

	if total == 1 {
		goos.Task = nil
		return
	}

	atomic.StoreUint32(&activeCores, uint32(total))

	goos.ProcID = ID
	// idle in WFE, rather than WFI, so that another core's semawakeup can
	// resume a parked M with SEV
	goos.Idle = ARM.EventIdleGovernor
	goos.Wake = ARM.Wake
	goos.Task = Task

	runtime.GOMAXPROCS(total)
}

// Task schedules a goroutine on a secondary core.
//
// On `GOOS=tamago` Go scheduler M's are not torn down and recreated;
// therefore, this function is invoked only once per secondary core
// (i.e. GOMAXPROCS-1 times).
func Task(sp, _, gp, fn unsafe.Pointer) {
	t := task{
		sp: uint64(uintptr(sp)),
		gp: uint64(uintptr(gp)),
		pc: uint64(uintptr(fn)),
	}
	k := atomic.AddUint32(&nextTaskCore, 1)

	if k >= atomic.LoadUint32(&activeCores) {
		panic("Task exceeds available resources")
	}

	coreID := uint64(taskCores[k])
	addr := taskAddress(coreID)

	if t.sp == 0 || t.gp == 0 {
		panic("Task empty")
	}

	// Publish gp/pc first and store sp last; the secondary core uses an acquire
	// load of sp as the readiness flag for the whole task slot.
	atomic.StoreUint64((*uint64)(unsafe.Pointer(uintptr(addr+8))), t.gp)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(uintptr(addr+16))), t.pc)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(uintptr(addr+0))), t.sp)

	// Wake the waiting secondary with SEV.
	ARM.SendEvent()
}

// funcPC returns the entry point address of a function.
//
//go:nosplit
func funcPC(fn func()) uint64 {
	return **(**uint64)(unsafe.Pointer(&fn))
}

// cleanU64 writes back the cache line holding p to the point of coherency so a
// core running with caches off can observe the value.
func cleanU64(p *uint64) {
	ARM.CleanDataCacheRange(uintptr(unsafe.Pointer(p)), unsafe.Sizeof(*p))
}
