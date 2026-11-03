//go:build !darwin && !linux

package sessionpressure

import "os"

func processPeakRSSMB() float64 { return 0 }

func processStatePeakRSSMB(*os.ProcessState) (float64, bool) { return 0, false }
