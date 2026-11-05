//go:build darwin

package sessionpressure

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Watch before the first locked eligibility check. A release between that check
// and select remains buffered. Loss or unsupported notification falls back to
// the authoritative two-second recheck; events never grant capacity themselves.
func watchWorkCapacity(dir string) (<-chan struct{}, func()) {
	noop := func() {}
	if os.Getenv("SESSION_PRESSURE_WORK_NOTIFY") == "0" {
		return nil, noop
	}
	f, err := os.OpenFile(filepath.Join(dir, workNotifyName), os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		return nil, noop
	}
	kq, err := unix.Kqueue()
	if err != nil {
		f.Close()
		return nil, noop
	}
	changes := []unix.Kevent_t{{Ident: uint64(f.Fd()), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_WRITE}, {Ident: 1, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR}}
	if _, err = unix.Kevent(kq, changes, nil, nil); err != nil {
		unix.Close(kq)
		f.Close()
		return nil, noop
	}
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	var once sync.Once
	var closeMu sync.Mutex
	closed := false
	go func() {
		defer close(done)
		defer func() {
			closeMu.Lock()
			defer closeMu.Unlock()
			closed = true
			unix.Close(kq)
		}()
		defer f.Close()
		events := make([]unix.Kevent_t, 2)
		for {
			n, err := unix.Kevent(kq, nil, events, nil)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return
			}
			for _, e := range events[:n] {
				if e.Filter == unix.EVFILT_USER {
					return
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake, func() {
		once.Do(func() {
			closeMu.Lock()
			if !closed {
				_, _ = unix.Kevent(kq, []unix.Kevent_t{{Ident: 1, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}, nil, nil)
			}
			closeMu.Unlock()
			<-done
		})
	}
}
