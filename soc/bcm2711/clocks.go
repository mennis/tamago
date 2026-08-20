// BCM2711 VideoCore clock, power and memory control
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ClockRate returns the current rate in Hz of the given clock, one of the
// CLOCK_ID_* constants.
//
// A rate of zero is ambiguous: the firmware answers zero both for a stopped
// clock and for an id it does not recognise, refusing neither. Cross-check with
// [MaxClockRate], which is nonzero for every clock that exists.
func ClockRate(id uint32) (hz uint32, err error) {
	buf := make([]byte, GET_CLOCK_RATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)

	buf, err = property(GET_CLOCK_RATE, buf, 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// MaxClockRate returns the highest rate in Hz the firmware will allow for the
// given clock. This is the ceiling SetClockRate clamps to, and is also the
// cheapest way to ask whether a clock id exists at all.
func MaxClockRate(id uint32) (hz uint32, err error) {
	buf := make([]byte, GET_MAX_CLOCK_RATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)

	buf, err = property(GET_MAX_CLOCK_RATE, buf, 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// ClockRateMeasured returns the rate in Hz the firmware measures the given
// clock actually running at, rather than the rate it believes it configured.
//
// It is the only number here that is a measurement rather than a report: a
// clock configured but not running measures zero while GET_CLOCK_RATE still
// answers its nominal value.
func ClockRateMeasured(id uint32) (hz uint32, err error) {
	buf := make([]byte, GET_CLOCK_RATE_MEASURED_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)

	buf, err = property(GET_CLOCK_RATE_MEASURED, buf, 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// SetClockRate requests a new rate in Hz for the given clock and returns the
// rate the firmware actually set.
//
// The returned rate is the one that matters: the firmware clamps to the limits
// it enforces and rounds to what the dividers express, so assuming the request
// was granted is how a UART ends up at the wrong baud.
//
// The skip-turbo word is sent as zero, so setting ARM_CLOCK may bring the other
// turbo mode clocks with it.
func SetClockRate(id uint32, hz uint32) (rate uint32, err error) {
	buf := make([]byte, SET_CLOCK_RATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)
	binary.LittleEndian.PutUint32(buf[4:], hz)
	binary.LittleEndian.PutUint32(buf[8:], 0) // do not skip setting turbo

	buf, err = property(SET_CLOCK_RATE, buf, 8)

	if err != nil {
		return
	}

	return binary.LittleEndian.Uint32(buf[4:]), nil
}

// EDIDBlock returns one 128-byte block of the attached display's EDID, block 0
// being the base block. It answers before any framebuffer has been allocated,
// which is the point: the mode has to be chosen first.
//
// A block past the end of the EDID comes back as an error rather than a refused
// message, so walk blocks upward until this fails. The base block's extension
// count, byte 126, says how many there should be.
func EDIDBlock(block uint32) (edid []byte, err error) {
	buf := make([]byte, GET_EDID_BLOCK_LEN)
	binary.LittleEndian.PutUint32(buf[0:], block)

	buf, err = property(GET_EDID_BLOCK, buf, GET_EDID_BLOCK_LEN)

	if err != nil {
		return
	}

	// The status word is signed: -1 is what a block the display does not have
	// comes back as.
	if status := int32(binary.LittleEndian.Uint32(buf[4:])); status != 0 {
		return nil, fmt.Errorf("bcm2711: EDID block %d unavailable (status %d)", block, status)
	}

	edid = make([]byte, 128)
	copy(edid, buf[8:])

	return
}

// Clock states, as GET_CLOCK_STATE and SET_CLOCK_STATE report them.
//
// Bit 1 means the same thing here as it does for power: the clock id is not one
// the firmware knows. It is reported rather than folded into bit 0 because "off"
// and "no such clock" are different diagnoses and GET_CLOCK_RATE cannot tell
// them apart — it answers zero for both (see ClockRate).
const (
	clockStateOn        = 1 << 0
	clockStateNotExists = 1 << 1
)

// ErrNoSuchClock is returned when the firmware answers a clock query with the
// "clock does not exist" bit set.
var ErrNoSuchClock = errors.New("bcm2711: no such clock id")

// ClockState reports the enable bit the firmware holds for the given clock.
//
// It is NOT a reliable answer to "is this clock running", which is worth being
// blunt about because the name invites that reading. Measured on a Pi 4 Model B
// from bare metal, every clock below read state 0 except UART -- including
// ARM, whose core was executing the code that asked:
//
//	ARM   state=false  measured=1500398464
//	CORE  state=false  measured=500000992
//	V3D   state=false  measured=500000992
//
// So a false here does not mean stopped. The bit appears to track an enable
// the firmware has been asked for rather than the physical state of the clock:
// SET_CLOCK_STATE alone did not move it, while SET_CLOCK_RATE did.
//
// GET_CLOCK_RATE is not the answer either, it reports the rate the clock would
// run at. GET_CLOCK_RATE_MEASURED is the closest thing to evidence, and even
// that reads 0 for clocks that must be running, SDRAM among them.
func ClockState(id uint32) (on bool, err error) {
	buf := make([]byte, GET_CLOCK_STATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)

	buf, err = property(GET_CLOCK_STATE, buf, 8)

	if err != nil {
		return
	}

	state := binary.LittleEndian.Uint32(buf[4:])

	if state&clockStateNotExists != 0 {
		return false, ErrNoSuchClock
	}

	return state&clockStateOn != 0, nil
}

// SetClockState enables or disables the given clock and returns an error if the
// reply does not echo the requested state.
//
// The echo is checked because it costs nothing, but do not read more into a nil
// return than that. Measured on a Pi 4 Model B: enabling V3D echoed on and
// returned nil, while GET_CLOCK_STATE went on reading false at +0, +10ms,
// +100ms and +1s, and the measured rate was unchanged at 500 MHz because the
// clock had been running all along. A later SET_CLOCK_RATE did move the state
// bit.
//
// Disabling is accepted just as readily, and is just as meaningless: switching
// off clock 2 (UART) returned nil and moved the state bit to false, over a
// serial console that carried on transmitting -- including the line that
// reported it. Switching off the ARM clock from the core executing the call
// returns nil too.
//
// So this reports what the firmware said, not what the hardware did. See
// [ClockState] for why neither query settles whether a clock runs.
func SetClockState(id uint32, on bool) error {
	var state uint32

	if on {
		state = clockStateOn
	}

	buf := make([]byte, SET_CLOCK_STATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], id)
	binary.LittleEndian.PutUint32(buf[4:], state)

	buf, err := property(SET_CLOCK_STATE, buf, 8)

	if err != nil {
		return err
	}

	got := binary.LittleEndian.Uint32(buf[4:])

	if got&clockStateNotExists != 0 {
		return ErrNoSuchClock
	}

	if got&clockStateOn != state&clockStateOn {
		return fmt.Errorf("bcm2711: clock %d stayed %v", id, got&clockStateOn != 0)
	}

	return nil
}

// Power-domain identifiers for SetPowerState and PowerState.
//
// These are the firmware's own device ids, not the device tree's power domain
// indices, which look similar and are a different namespace. Both are small
// numbers and they refer to different things.
//
// The firmware answers for ids 0 through 10 with the does-not-exist bit clear
// and for 11 upward with it set, so 10 is the top of the table.
const (
	POWER_DEVICE_SD_CARD = 0
	POWER_DEVICE_UART0   = 1
	POWER_DEVICE_UART1   = 2
	POWER_DEVICE_USB_HCD = 3
	POWER_DEVICE_I2C0    = 4
	POWER_DEVICE_I2C1    = 5
	POWER_DEVICE_I2C2    = 6
	POWER_DEVICE_SPI     = 7
	POWER_DEVICE_CCP2TX  = 8

	// POWER_DEVICE_V3D is accepted by this firmware and does not power V3D.
	// Setting it on returns success and reports the device still off, with the
	// does-not-exist bit clear, while the identical exchange against the SD
	// card device reports on. The tag works; this device is inert.
	//
	// It is defined because it is the documented id and a caller will look for
	// it, not because it does anything. See
	// SetPowerState.
	POWER_DEVICE_V3D = 10
)

// Power states, as SET_POWER_STATE and GET_POWER_STATE encode them. Note that
// bit 1 means different things in the two directions: on the way in it asks the
// firmware to block until the transition is complete, and on the way out it
// reports that the device id is not one the firmware knows.
const (
	powerStateOn        = 1 << 0
	powerStateWait      = 1 << 1
	powerStateNotExists = 1 << 1
)

// ErrNoSuchPowerDevice is returned when the firmware answers a power query with
// the "device does not exist" bit set.
var ErrNoSuchPowerDevice = errors.New("bcm2711: no such power device id")

// PowerState reports whether the firmware considers the given device powered.
func PowerState(device uint32) (on bool, err error) {
	buf := make([]byte, GET_POWER_STATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], device)

	buf, err = property(GET_POWER_STATE, buf, 8)

	if err != nil {
		return
	}

	state := binary.LittleEndian.Uint32(buf[4:])

	if state&powerStateNotExists != 0 {
		return false, ErrNoSuchPowerDevice
	}

	return state&powerStateOn != 0, nil
}

// SetPowerState powers the given device on or off, asking the firmware to block
// until the transition is complete, and returns an error if the state it
// reports afterwards is not the one requested.
//
// The wait bit is set because reading a register in a block whose rail is still
// settling does not fault, it returns plausible garbage. The applied state is
// checked rather than assumed.
//
// The V3D domain does not respond to this tag, and Linux does not use it for
// V3D either: the device tree gives the GPU its power domain and reset on the
// MMIO PM block and takes only its clock from the firmware. A bring-up that
// needs V3D powered should drive the PM block and use [SetClockState].
func SetPowerState(device uint32, on bool) error {
	state := uint32(powerStateWait)

	if on {
		state |= powerStateOn
	}

	buf := make([]byte, SET_POWER_STATE_LEN)
	binary.LittleEndian.PutUint32(buf[0:], device)
	binary.LittleEndian.PutUint32(buf[4:], state)

	buf, err := property(SET_POWER_STATE, buf, 8)

	if err != nil {
		return err
	}

	got := binary.LittleEndian.Uint32(buf[4:])

	if got&powerStateNotExists != 0 {
		return ErrNoSuchPowerDevice
	}

	if got&powerStateOn != state&powerStateOn {
		return fmt.Errorf("bcm2711: power device %d stayed %v", device, got&powerStateOn != 0)
	}

	return nil
}

// VideoCore allocation flags for AllocateGPUMemory.
//
// Bits [3:2] are a two-bit field selecting a cache alias, not two independent
// flags, though the names read as though they were: combining DIRECT and
// COHERENT does not give memory that is both, it gives L1_NONALLOCATING.
//
// Measured by allocating at each setting and reading back the bus address:
//
//	MEM_FLAG_DIRECT            (0x4)  -> bus 0xfeae9000, alias 0xc0000000
//	MEM_FLAG_COHERENT          (0x8)  -> bus 0xbeae9000, alias 0x80000000
//	MEM_FLAG_L1_NONALLOCATING  (0xc)  -> bus 0xbeae9000, alias 0x80000000
//
// The same physical page in three cases and two different aliases. DIRECT is
// the uncached one a CPU writer without cache maintenance wants; the other two
// land in an L2 cached window and need the data cache cleaned before the GPU
// reads what was written.
const (
	MEM_FLAG_DISCARDABLE = 1 << 0 // may be resized to zero at any time
	MEM_FLAG_NORMAL      = 0 << 2 // normal allocating alias; not for ARM use
	MEM_FLAG_DIRECT      = 1 << 2 // 0xc alias, uncached
	MEM_FLAG_COHERENT    = 2 << 2 // 0x8 alias, non-allocating in L1 but coherent

	// MEM_FLAG_L1_NONALLOCATING is the DIRECT|COHERENT value, allocating in
	// L2. Spelled out rather than left to the caller to OR together, because
	// the OR reads like a combination and is not one.
	MEM_FLAG_L1_NONALLOCATING = MEM_FLAG_DIRECT | MEM_FLAG_COHERENT

	MEM_FLAG_ZERO           = 1 << 4 // zero the allocation
	MEM_FLAG_NO_INIT        = 1 << 5 // leave it uninitialised (default is all ones)
	MEM_FLAG_HINT_PERMALOCK = 1 << 6 // will be locked for a long time
)

// ErrAllocationRefused is returned when the firmware processes an allocation
// request and grants nothing.
var ErrAllocationRefused = errors.New("bcm2711: VideoCore refused the allocation")

// AllocateGPUMemory reserves size bytes of contiguous memory in the VideoCore's
// carve-out and returns an opaque handle to it.
//
// The memory has no address yet: a handle is a reservation, [LockGPUMemory]
// pins it and produces the bus address the GPU can reach, and
// [ReleaseGPUMemory] gives it back. Nothing here maps it for the CPU.
//
// alignment is in bytes and must be a power of two; flags select the cache alias
// and the initialisation, from the MEM_FLAG_* constants above. A zero handle is
// the firmware's way of refusing, and is reported as an error rather than passed
// on: it is not distinguishable from a valid handle by type, and a caller that
// locks handle 0 gets bus address 0 back and hands the GPU a null pointer.
//
// The allocation comes out of the region gpu_mem sizes, NOT out of the ARM's
// RAM, so it neither reduces nor is limited by the Go heap. It is also the only
// memory here that is guaranteed reachable through the GPU's bus alias, which
// covers the low gigabyte and no more (see GPUBusLimit).
func AllocateGPUMemory(size, alignment, flags uint32) (handle uint32, err error) {
	buf := make([]byte, ALLOCATE_MEMORY_LEN)
	binary.LittleEndian.PutUint32(buf[0:], size)
	binary.LittleEndian.PutUint32(buf[4:], alignment)
	binary.LittleEndian.PutUint32(buf[8:], flags)

	buf, err = property(ALLOCATE_MEMORY, buf, 4)

	if err != nil {
		return
	}

	if handle = binary.LittleEndian.Uint32(buf); handle == 0 {
		return 0, ErrAllocationRefused
	}

	return
}

// LockGPUMemory pins a handle and returns the BUS address of its memory.
//
// The returned address is not an ARM physical address and must not be
// dereferenced. Bits [31:30] select which of the VideoCore's cache aliases of
// the same DRAM the address refers to, chosen by the flags the allocation was
// made with (see the MEM_FLAG_* constants); the physical address is what remains
// once they are cleared, which is what GPU_BUS_OFFSET is for. Hand the bus
// address to the GPU and the physical address to the CPU, and never the reverse:
// a bus address dereferenced by the ARM lands roughly a gigabyte past where the
// caller meant, in a region that is not RAM.
//
// The lock is what makes the address meaningful. The firmware is free to move an
// unlocked allocation, so an address read once and cached across an unlock is a
// stale pointer into memory something else now owns.
//
// A zero return is the firmware refusing, and is reported as an error for the
// same reason a zero handle is.
func LockGPUMemory(handle uint32) (bus uint32, err error) {
	buf := make([]byte, LOCK_MEMORY_LEN)
	binary.LittleEndian.PutUint32(buf[0:], handle)

	buf, err = property(LOCK_MEMORY, buf, 4)

	if err != nil {
		return
	}

	if bus = binary.LittleEndian.Uint32(buf); bus == 0 {
		return 0, fmt.Errorf("bcm2711: VideoCore refused to lock handle %#x", handle)
	}

	return
}

// UnlockGPUMemory unpins a handle. The bus address the matching LockGPUMemory
// returned is dead the moment this returns — the firmware may relocate the
// allocation — so a caller holding one must drop it rather than re-lock and
// assume it is unchanged.
//
// The firmware reports failure in a status word rather than by refusing the
// message, so a nonzero status becomes an error here. Zero is success.
func UnlockGPUMemory(handle uint32) error {
	return memoryStatus(UNLOCK_MEMORY, UNLOCK_MEMORY_LEN, handle, "unlock")
}

// ReleaseGPUMemory frees a handle's memory. It does not unlock it first: a
// caller that locked must unlock, because the firmware's own bookkeeping is what
// this is talking to and skipping a step leaks the carve-out one allocation at a
// time, with no error and no way to enumerate what was lost short of a reboot.
func ReleaseGPUMemory(handle uint32) error {
	return memoryStatus(RELEASE_MEMORY, RELEASE_MEMORY_LEN, handle, "release")
}

// memoryStatus performs a handle-in, status-out memory tag exchange.
func memoryStatus(id uint32, length int, handle uint32, verb string) error {
	buf := make([]byte, length)
	binary.LittleEndian.PutUint32(buf[0:], handle)

	buf, err := property(id, buf, 4)

	if err != nil {
		return err
	}

	if status := binary.LittleEndian.Uint32(buf); status != 0 {
		return fmt.Errorf("bcm2711: VideoCore refused to %s handle %#x (status %#x)",
			verb, handle, status)
	}

	return nil
}
