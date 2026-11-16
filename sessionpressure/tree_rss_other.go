//go:build !darwin && !linux

package sessionpressure

import "errors"

func listProcessRows() ([]processRow, error) {
	return nil, errors.New("process tree listing unsupported on this platform")
}

var processRSSKB func(pid int) (int64, bool)
