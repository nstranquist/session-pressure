package sessionpressure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const workNotifyName = "work-capacity.notify"

// This signature excludes heartbeats and sampled pressure. A blocked waiter
// updating its heartbeat must not wake every other waiter into a write storm.
func workCapacitySignature(s workState) string {
	var b strings.Builder
	for _, l := range s.Leases {
		fmt.Fprintf(&b, "l:%s:%d;", l.ID, l.chargedWeight())
	}
	for _, w := range s.Waiters {
		fmt.Fprintf(&b, "w:%s:%s;", w.OperationID, w.ReservationKind)
	}
	fmt.Fprint(&b, s.OverrideOperationID, s.OverrideQueue)
	return b.String()
}
func notifyWorkCapacity(dir string) {
	if os.Getenv("SESSION_PRESSURE_WORK_NOTIFY") == "0" {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, workNotifyName), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	// Fixed inode and size: no accumulating log or watcher re-registration race.
	_, _ = f.WriteAt([]byte{1}, 0)
}
