// BCM2712 SoC timer support
// https://github.com/usbarmory/tamago
//
// Copyright (c) The TamaGo Authors. All Rights Reserved.
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.

package bcm2712

import (
	"time"
)

// WatchdogPeriod is the fixed 16us tick period of the BCM2712 watchdog.
const WatchdogPeriod = uint64(16 * time.Microsecond)
