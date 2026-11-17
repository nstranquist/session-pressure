package sessionpressure

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func envValue(environment []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range environment {
		if key, v, ok := strings.Cut(entry, "="); ok && key == name {
			value, found = v, true
		}
	}
	return value, found
}

func TestWorkThrottleFromEnv(t *testing.T) {
	throttle, err := WorkThrottleFromEnv([]string{"PATH=/bin", WorkThrottleCoresEnv + "=3", WorkThrottleIOEnv + "=1"})
	if err != nil || throttle.Cores != 3 || !throttle.BackgroundIO || !throttle.Active() {
		t.Fatalf("throttle = %+v, err = %v", throttle, err)
	}
	if throttle, err := WorkThrottleFromEnv([]string{"PATH=/bin", WorkThrottleCoresEnv + "=", WorkThrottleIOEnv + "=0"}); err != nil || throttle.Active() {
		t.Fatalf("unset throttle = %+v, err = %v", throttle, err)
	}
	for _, bad := range []string{WorkThrottleCoresEnv + "=0", WorkThrottleCoresEnv + "=-2", WorkThrottleCoresEnv + "=half", WorkThrottleIOEnv + "=yes"} {
		if _, err := WorkThrottleFromEnv([]string{bad}); err == nil {
			t.Errorf("%s must be refused, not ignored", bad)
		}
	}
}

func TestWorkThrottleEnvironment(t *testing.T) {
	base := []string{"PATH=/bin", "GOFLAGS=-mod=mod -p=12", "MAKEFLAGS=-k -j", "CARGO_BUILD_JOBS=2", "RAYON_NUM_THREADS=16", "OMP_NUM_THREADS=auto"}
	original := slices.Clone(base)
	environment := WorkThrottle{Cores: 4}.Environment(base)
	if !slices.Equal(base, original) {
		t.Fatal("Environment changed its input")
	}
	want := map[string]string{
		"PATH": "/bin", "GOMAXPROCS": "4", "GOFLAGS": "-mod=mod -p=4", "MAKEFLAGS": "-k -j4",
		"CARGO_BUILD_JOBS": "2", "RAYON_NUM_THREADS": "4", "OMP_NUM_THREADS": "4", "CMAKE_BUILD_PARALLEL_LEVEL": "4",
		workThrottleAppliedEnv: "4",
	}
	for name, value := range want {
		if got, ok := envValue(environment, name); !ok || got != value {
			t.Errorf("%s = %q (set %t), want %q", name, got, ok, value)
		}
	}
	seen := map[string]int{}
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		seen[name]++
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("%s appears %d times", name, count)
		}
	}
	// A caller's lower limits are kept.
	lower := WorkThrottle{Cores: 8}.Environment([]string{"GOMAXPROCS=2", "GOFLAGS=-p=3", "MAKEFLAGS=-j2"})
	for name, value := range map[string]string{"GOMAXPROCS": "2", "GOFLAGS": "-p=3", "MAKEFLAGS": "-j2"} {
		if got, _ := envValue(lower, name); got != value {
			t.Errorf("lower %s = %q, want %q kept", name, got, value)
		}
	}
	// No CPU throttle leaves the environment alone, as the same slice.
	untouched := WorkThrottle{BackgroundIO: true}.Environment(base)
	if !slices.Equal(untouched, base) {
		t.Fatal("an I/O-only throttle changed the environment")
	}
}

func TestWorkThrottleIOWrapper(t *testing.T) {
	found := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	throttle := WorkThrottle{Cores: 2, BackgroundIO: true}
	if got := throttle.IOWrapper("darwin", found); !slices.Equal(got, []string{"/usr/bin/taskpolicy", "-d", "throttle"}) {
		t.Fatalf("darwin wrapper = %v", got)
	}
	if got := throttle.IOWrapper("linux", found); !slices.Equal(got, []string{"/usr/bin/ionice", "-c", "3"}) {
		t.Fatalf("linux wrapper = %v", got)
	}
	if got := throttle.IOWrapper("darwin", missing); got != nil {
		t.Fatalf("missing tool wrapper = %v, want none", got)
	}
	if got := throttle.IOWrapper("plan9", found); got != nil {
		t.Fatalf("unknown host wrapper = %v, want none", got)
	}
	if got := (WorkThrottle{Cores: 2}).IOWrapper("darwin", found); got != nil {
		t.Fatalf("cpu-only wrapper = %v, want none", got)
	}

	executable, args, environment := throttle.Apply("go", []string{"test", "./..."}, []string{"PATH=/bin"}, "darwin", found)
	if executable != "/usr/bin/taskpolicy" || !slices.Equal(args, []string{"-d", "throttle", "go", "test", "./..."}) {
		t.Fatalf("applied command = %s %v", executable, args)
	}
	if got, _ := envValue(environment, "GOMAXPROCS"); got != "2" {
		t.Fatalf("applied GOMAXPROCS = %q", got)
	}
	executable, args, _ = throttle.Apply("go", []string{"test"}, nil, "darwin", missing)
	if executable != "go" || !slices.Equal(args, []string{"test"}) {
		t.Fatalf("command without a wrapper tool = %s %v", executable, args)
	}
}
