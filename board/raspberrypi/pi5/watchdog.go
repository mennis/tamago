// Raspberry Pi 5 watchdog timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package pi5

import (
	"github.com/usbarmory/tamago/soc/bcm2712"
)

// Watchdog is the BCM2712 hardware watchdog timer, which resets the board if
// not fed with Reset within the timeout passed to Start.
var Watchdog = bcm2712.Watchdog
