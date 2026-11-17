package sessionpressure

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
)

// automatedBrowserKernelArgs reads the Darwin argc/executable/argv layout.
// It scans all argv entries, including flags after the identity reader's 64
// argument projection, and never interprets environment entries as flags.
func automatedBrowserKernelArgs(body []byte) (bool, error) {
	automated, _, err := browserKernelArgs(body)
	return automated, err
}

// browserKernelArgs also returns the root's --user-data-dir, which locates a
// snapref-managed session record (browser_managed_session.go). No other
// argument is kept.
func browserKernelArgs(body []byte) (bool, string, error) {
	invalid := errors.New("invalid browser kernel arguments")
	if len(body) < 5 || len(body) > 4<<20 {
		return false, "", invalid
	}
	argc := int(binary.LittleEndian.Uint32(body[:4]))
	if argc < 1 || argc > 16384 {
		return false, "", invalid
	}
	end := bytes.IndexByte(body[4:], 0)
	if end < 0 {
		return false, "", invalid
	}
	executable := string(body[4 : 4+end])
	chrome := strings.HasSuffix(executable, "/Contents/MacOS/Google Chrome") ||
		strings.HasSuffix(executable, "/Contents/MacOS/Google Chrome for Testing") ||
		filepath.Base(executable) == "chromium" || filepath.Base(executable) == "google-chrome"
	if !chrome {
		return false, "", nil
	}
	index := 4 + end + 1
	for index < len(body) && body[index] == 0 {
		index++
	}
	automated, child, userDataDir := false, false, ""
	for arg := 0; arg < argc; arg++ {
		if index >= len(body) {
			return false, "", invalid
		}
		n := bytes.IndexByte(body[index:], 0)
		if n < 0 {
			return false, "", invalid
		}
		value := body[index : index+n]
		if arg > 0 {
			child = child || bytes.HasPrefix(value, []byte("--type="))
			automated = automated || bytes.Equal(value, []byte("--headless")) || bytes.HasPrefix(value, []byte("--headless=")) ||
				bytes.Equal(value, []byte("--remote-debugging-port")) || bytes.HasPrefix(value, []byte("--remote-debugging-port=")) ||
				bytes.Equal(value, []byte("--remote-debugging-pipe"))
			if dir, ok := bytes.CutPrefix(value, []byte("--user-data-dir=")); ok {
				userDataDir = string(dir)
			}
		}
		index += n + 1
	}
	if !automated || child {
		return false, "", nil
	}
	return true, userDataDir, nil
}
