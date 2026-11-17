package sessionpressure

import (
	"math/rand"
	"testing"
)

// The clean fast path returns exactly what the full sanitizer returns.
func TestPrivacySafeExecutableFastPathMatchesSanitizer(t *testing.T) {
	cases := []string{
		"", ".", "..", "/", " ", "_", "__a__", "a", "go", "sqlite3", "Google Chrome Helper (Renderer)",
		"com.apple.WebKit.WebContent", "node", "kernel_task", "a/b", "/usr/bin/swift", " padded ", "x+y-z[1](2)",
		"ünïcode", "名前", "tab\tname", "1234567890123456789012345678901234567890123456789012345678901234",
		"12345678901234567890123456789012345678901234567890123456789012345", "-", "(", "a.b", ".hidden", "trailing.",
	}
	alphabet := []rune("abcXYZ019._+-()[]_ /\\\tüé名\x00\x7f")
	random := rand.New(rand.NewSource(1))
	for range 20_000 {
		length := random.Intn(70)
		runes := make([]rune, length)
		for index := range runes {
			runes[index] = alphabet[random.Intn(len(alphabet))]
		}
		cases = append(cases, string(runes))
	}
	for _, value := range cases {
		if got, want := privacySafeExecutable(value), privacySafeExecutableSanitize(value); got != want {
			t.Fatalf("privacySafeExecutable(%q) = %q, sanitizer = %q", value, got, want)
		}
	}
	if !privacySafeExecutableClean("sqlite3") || privacySafeExecutableClean("kernel_task") {
		t.Fatal("fast path classification is wrong for common names")
	}
}
