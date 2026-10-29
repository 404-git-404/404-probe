//go:build !windows

package server

import "time"

func dashboardPerfMilliseconds() float64 { return float64(time.Now().UnixNano()) / 1e6 }
