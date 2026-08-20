// BCM2711 VideoCore property tags
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2711

// Property-interface tag identifiers, see
// https://github.com/raspberrypi/firmware/wiki/Mailbox-property-interface
//
// Each tag is paired with a _LEN giving the size its value buffer must be
// allocated at, which is the larger of the request and the response: the
// firmware writes its answer over the buffer it was handed, and one sized for
// the request alone truncates the reply.
//
// The identifier encodes direction in bits [17:16] of its low half, 0 for get
// and 8 for set, which is why a set and a get tag differ by 0x8000 and share a
// length.
//
// GET_GENCMD_RESULT is deliberately absent. The firmware accepts the tag and
// answers, but the reply is four bytes of 0xff for every command string,
// including ones vcgencmd runs happily under Linux.
const (
	// Firmware and board identity.

	GET_FIRMWARE_REVISION     = 0x00000001
	GET_FIRMWARE_REVISION_LEN = 4

	GET_BOARD_MODEL     = 0x00010001
	GET_BOARD_MODEL_LEN = 4

	GET_BOARD_REVISION     = 0x00010002
	GET_BOARD_REVISION_LEN = 4

	GET_BOARD_MAC_ADDRESS     = 0x00010003
	GET_BOARD_MAC_ADDRESS_LEN = 6

	GET_BOARD_SERIAL     = 0x00010004
	GET_BOARD_SERIAL_LEN = 8

	GET_ARM_MEMORY     = 0x00010005
	GET_ARM_MEMORY_LEN = 8

	GET_VC_MEMORY     = 0x00010006
	GET_VC_MEMORY_LEN = 8

	GET_CLOCKS     = 0x00010007
	GET_CLOCKS_LEN = 8

	GET_COMMAND_LINE = 0x00050001

	GET_DMA_CHANNELS     = 0x00060001
	GET_DMA_CHANNELS_LEN = 4

	// Power, clocks, voltage, temperature. Every one of these takes the device
	// or clock id as the first word of the request, so the value buffer is
	// never empty even for a "get".

	GET_POWER_STATE     = 0x00020001
	GET_POWER_STATE_LEN = 8

	GET_TIMING     = 0x00020002
	GET_TIMING_LEN = 8

	SET_POWER_STATE     = 0x00028001
	SET_POWER_STATE_LEN = 8

	GET_CLOCK_STATE     = 0x00030001
	GET_CLOCK_STATE_LEN = 8

	SET_CLOCK_STATE     = 0x00038001
	SET_CLOCK_STATE_LEN = 8

	GET_CLOCK_RATE     = 0x00030002
	GET_CLOCK_RATE_LEN = 8

	// SET_CLOCK_RATE is the odd one: the request is three words (clock id,
	// rate, skip-setting-turbo) and the response only two, so the buffer is
	// sized by the request.
	SET_CLOCK_RATE     = 0x00038002
	SET_CLOCK_RATE_LEN = 12

	GET_MAX_CLOCK_RATE     = 0x00030004
	GET_MAX_CLOCK_RATE_LEN = 8

	GET_MIN_CLOCK_RATE     = 0x00030007
	GET_MIN_CLOCK_RATE_LEN = 8

	GET_CLOCK_RATE_MEASURED     = 0x00030047
	GET_CLOCK_RATE_MEASURED_LEN = 8

	GET_TURBO     = 0x00030009
	GET_TURBO_LEN = 8

	SET_TURBO     = 0x00038009
	SET_TURBO_LEN = 8

	GET_VOLTAGE     = 0x00030003
	GET_VOLTAGE_LEN = 8

	SET_VOLTAGE     = 0x00038003
	SET_VOLTAGE_LEN = 8

	GET_MAX_VOLTAGE     = 0x00030005
	GET_MAX_VOLTAGE_LEN = 8

	GET_MIN_VOLTAGE     = 0x00030008
	GET_MIN_VOLTAGE_LEN = 8

	GET_TEMPERATURE     = 0x00030006
	GET_TEMPERATURE_LEN = 8

	GET_MAX_TEMPERATURE     = 0x0003000a
	GET_MAX_TEMPERATURE_LEN = 8

	// GET_THROTTLED reports under-voltage and frequency capping, both as
	// happening now and as having happened since boot. It is the same word
	// vcgencmd get_throttled returns under Linux.
	//
	// It takes no request payload but the response is a word, so the buffer is
	// four bytes and the reply is read out of the same place. Unlike the
	// temperature and clock tags there is no sensor or clock id to send.
	GET_THROTTLED     = 0x00030046
	GET_THROTTLED_LEN = 4

	// VideoCore memory allocation.

	ALLOCATE_MEMORY     = 0x0003000c
	ALLOCATE_MEMORY_LEN = 12

	LOCK_MEMORY     = 0x0003000d
	LOCK_MEMORY_LEN = 4

	UNLOCK_MEMORY     = 0x0003000e
	UNLOCK_MEMORY_LEN = 4

	RELEASE_MEMORY     = 0x0003000f
	RELEASE_MEMORY_LEN = 4

	// GET_EDID_BLOCK reads one 128-byte block of the attached display's EDID.
	// The value buffer is [u32 block number][u32 status][128 bytes], hence 136
	// — the request occupies only the first word of it.
	GET_EDID_BLOCK     = 0x00030020
	GET_EDID_BLOCK_LEN = 136

	// Framebuffer. FB_ALLOCATE_BUFFER's request word is an alignment request
	// and its response is base+size, which is why an 8-byte buffer carries a
	// 4-byte request. FB_RELEASE_BUFFER genuinely has no value at all.

	FB_ALLOCATE_BUFFER     = 0x00040001
	FB_ALLOCATE_BUFFER_LEN = 8

	FB_RELEASE_BUFFER     = 0x00048001
	FB_RELEASE_BUFFER_LEN = 0

	FB_BLANK     = 0x00040002
	FB_BLANK_LEN = 4

	FB_GET_PHYSICAL_WH     = 0x00040003
	FB_GET_PHYSICAL_WH_LEN = 8

	FB_TEST_PHYSICAL_WH     = 0x00044003
	FB_TEST_PHYSICAL_WH_LEN = 8

	FB_SET_PHYSICAL_WH     = 0x00048003
	FB_SET_PHYSICAL_WH_LEN = 8

	FB_GET_VIRTUAL_WH     = 0x00040004
	FB_GET_VIRTUAL_WH_LEN = 8

	FB_TEST_VIRTUAL_WH     = 0x00044004
	FB_TEST_VIRTUAL_WH_LEN = 8

	FB_SET_VIRTUAL_WH     = 0x00048004
	FB_SET_VIRTUAL_WH_LEN = 8

	FB_GET_DEPTH     = 0x00040005
	FB_GET_DEPTH_LEN = 4

	FB_TEST_DEPTH     = 0x00044005
	FB_TEST_DEPTH_LEN = 4

	FB_SET_DEPTH     = 0x00048005
	FB_SET_DEPTH_LEN = 4

	FB_GET_PIXEL_ORDER     = 0x00040006
	FB_GET_PIXEL_ORDER_LEN = 4

	FB_TEST_PIXEL_ORDER     = 0x00044006
	FB_TEST_PIXEL_ORDER_LEN = 4

	FB_SET_PIXEL_ORDER     = 0x00048006
	FB_SET_PIXEL_ORDER_LEN = 4

	FB_GET_ALPHA_MODE     = 0x00040007
	FB_GET_ALPHA_MODE_LEN = 4

	FB_TEST_ALPHA_MODE     = 0x00044007
	FB_TEST_ALPHA_MODE_LEN = 4

	FB_SET_ALPHA_MODE     = 0x00048007
	FB_SET_ALPHA_MODE_LEN = 4

	// FB_GET_PITCH is read-only by construction: the firmware decides the
	// stride and there is no set or test form of it. A caller that computes
	// pitch as width*bpp/8 instead of asking is wrong on any mode the hardware
	// pads.
	FB_GET_PITCH     = 0x00040008
	FB_GET_PITCH_LEN = 4

	FB_GET_VIRTUAL_OFFSET     = 0x00040009
	FB_GET_VIRTUAL_OFFSET_LEN = 8

	FB_TEST_VIRTUAL_OFFSET     = 0x00044009
	FB_TEST_VIRTUAL_OFFSET_LEN = 8

	FB_SET_VIRTUAL_OFFSET     = 0x00048009
	FB_SET_VIRTUAL_OFFSET_LEN = 8

	FB_GET_OVERSCAN     = 0x0004000a
	FB_GET_OVERSCAN_LEN = 16

	FB_TEST_OVERSCAN     = 0x0004400a
	FB_TEST_OVERSCAN_LEN = 16

	FB_SET_OVERSCAN     = 0x0004800a
	FB_SET_OVERSCAN_LEN = 16

	FB_GET_PALETTE     = 0x0004000b
	FB_GET_PALETTE_LEN = 1024

	FB_TEST_PALETTE = 0x0004400b
	FB_SET_PALETTE  = 0x0004800b
)

// Clock identifiers for the GET_CLOCK_RATE / SET_CLOCK_RATE family.
//
// These are firmware ids and bear no relation to the CPRMAN register offsets;
// the same clock has a different number in each namespace. An id the firmware
// does not know is not refused — the exchange succeeds and the rate comes back
// zero, so a zero rate means "no such clock" as often as it means "stopped".
const (
	CLOCK_ID_EMMC      = 1
	CLOCK_ID_UART      = 2
	CLOCK_ID_ARM       = 3
	CLOCK_ID_CORE      = 4
	CLOCK_ID_V3D       = 5
	CLOCK_ID_H264      = 6
	CLOCK_ID_ISP       = 7
	CLOCK_ID_SDRAM     = 8
	CLOCK_ID_PIXEL     = 9
	CLOCK_ID_PWM       = 10
	CLOCK_ID_HEVC      = 11
	CLOCK_ID_EMMC2     = 12
	CLOCK_ID_M2MC      = 13
	CLOCK_ID_PIXEL_BVB = 14
)
