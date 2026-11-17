package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Plant an independently owned live lock to exercise cross-process polling,
// rather than the in-process FIFO. Run the compiled benchmark on the host.
func BenchmarkShortLockHandoff(b *testing.B) {
	target := filepath.Join(b.TempDir(), "short-writer")
	lock := target + ".lock"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		raw := []byte(fmt.Sprintf(`{"pid":%d,"timestamp":%d}`, os.Getpid(), time.Now().UnixMilli()))
		if err := os.WriteFile(lock, raw, 0600); err != nil {
			b.Fatal(err)
		}
		done := make(chan error, 1)
		b.StartTimer()
		go func() {
			time.Sleep(2 * time.Millisecond)
			done <- os.Remove(lock)
		}()
		unlock, err := Acquire(target, time.Second)
		if err != nil {
			b.Fatal(err)
		}
		unlock()
		if err := <-done; err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUncontendedAcquire measures one acquire and release with no
// competitor, for each publish path, in the same binary and run.
func BenchmarkUncontendedAcquire(b *testing.B) {
	paths := []struct {
		name   string
		rename func(from, to string) error
	}{{"hard-link", nil}}
	if platformExclusiveRename != nil {
		paths = append(paths, struct {
			name   string
			rename func(from, to string) error
		}{"exclusive-rename", platformExclusiveRename})
	}
	for _, path := range paths {
		b.Run(path.name, func(b *testing.B) {
			forcePublish(b, path.rename)
			target := filepath.Join(b.TempDir(), "uncontended")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				unlock, err := Acquire(target, time.Second)
				if err != nil {
					b.Fatal(err)
				}
				unlock()
			}
		})
	}
}
