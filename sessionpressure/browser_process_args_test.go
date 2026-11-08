package sessionpressure

import (
	"encoding/binary"
	"strings"
	"testing"
)

func browserKernelArgsFixture(executable string, args, environment []string) []byte {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, uint32(len(args)))
	body = append(body, executable...)
	body = append(body, 0, 0, 0)
	for _, arg := range append(append([]string(nil), args...), environment...) {
		body = append(body, arg...)
		body = append(body, 0)
	}
	return body
}

func TestBrowserKernelArgsSeparatesRootsChildrenAndPersonalSessions(t *testing.T) {
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	late := append([]string{chrome}, make([]string, 80)...)
	for i := 1; i < len(late); i++ {
		late[i] = "--unrelated=value"
	}
	late = append(late, "--remote-debugging-pipe")
	for _, tc := range []struct {
		name       string
		executable string
		args, env  []string
		want       bool
	}{
		{name: "personal", executable: chrome, args: []string{chrome}},
		{name: "headless", executable: chrome, args: []string{chrome, "--headless=new"}, want: true},
		{name: "port", executable: chrome, args: []string{chrome, "--remote-debugging-port=9222"}, want: true},
		{name: "late flag", executable: chrome, args: late, want: true},
		{name: "child", executable: chrome, args: append(append([]string(nil), late...), "--type=gpu-process")},
		{name: "environment", executable: chrome, args: []string{chrome}, env: []string{"--headless=new"}},
		{name: "url text", executable: chrome, args: []string{chrome, "https://example.invalid/ --headless=new"}},
		{name: "other executable", executable: "/usr/bin/python3", args: []string{"python3", "--headless"}},
		{name: "spoofed argv0", executable: "/usr/bin/python3", args: []string{chrome, "--headless"}},
		{name: "chromium", executable: "/opt/bin/chromium", args: []string{"chromium", "--remote-debugging-pipe"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := automatedBrowserKernelArgs(browserKernelArgsFixture(tc.executable, tc.args, tc.env))
			if err != nil || got != tc.want {
				t.Fatalf("automated=%v err=%v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestBrowserKernelArgsRejectsMalformedInput(t *testing.T) {
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	valid := browserKernelArgsFixture(chrome, []string{chrome, "--headless"}, nil)
	for _, body := range [][]byte{nil, {1, 2, 3, 4}, valid[:len(valid)-1], []byte(strings.Repeat("x", (4<<20)+1))} {
		if got, err := automatedBrowserKernelArgs(body); err == nil || got {
			t.Fatalf("accepted malformed %d-byte input", len(body))
		}
	}
}
