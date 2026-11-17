package sessionpressure

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// WorkThrottle is the "admit throttled" step of resource-headroom admission
// (work_resource_headroom.go): the command runs, with less parallelism and,
// when disk writes are high, at a lower disk I/O priority.
//
// Admission does not apply it. Shadow decisions only record the throttle they
// would ask for. The mechanism runs when the caller opts in for one command
// with NDEV_WORK_THROTTLE_CORES (and NDEV_WORK_THROTTLE_IO=1), so it can be
// measured before any policy uses it.
//
// Parallelism is lowered through the build tools' own environment contracts.
// A tool that reads none of them runs unthrottled; the lease's measured CPU
// then shows it, and the cost model learns the real figure.
type WorkThrottle struct {
	// Cores is the parallelism limit; 0 means no CPU throttle.
	Cores int `json:"cores,omitempty"`
	// BackgroundIO lowers the command's disk I/O priority.
	BackgroundIO bool `json:"background_io,omitempty"`
}

const (
	WorkThrottleCoresEnv = "NDEV_WORK_THROTTLE_CORES"
	WorkThrottleIOEnv    = "NDEV_WORK_THROTTLE_IO"
	// workThrottleAppliedEnv tells the command and nested wrappers which
	// limit is in force.
	workThrottleAppliedEnv = "NDEV_WORK_THROTTLE_APPLIED_CORES"
)

func (throttle WorkThrottle) Active() bool { return throttle.Cores > 0 || throttle.BackgroundIO }

// WorkThrottleFromEnv reads the caller's opt-in. An invalid value is an
// error: a typo must not silently run unthrottled.
func WorkThrottleFromEnv(environment []string) (WorkThrottle, error) {
	var throttle WorkThrottle
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch name {
		case WorkThrottleCoresEnv:
			if value == "" {
				continue
			}
			cores, err := strconv.Atoi(value)
			if err != nil || cores < 1 {
				return WorkThrottle{}, fmt.Errorf("%s must be a whole number of cores, 1 or more: %q", WorkThrottleCoresEnv, value)
			}
			throttle.Cores = cores
		case WorkThrottleIOEnv:
			switch value {
			case "1", "true":
				throttle.BackgroundIO = true
			case "", "0", "false":
			default:
				return WorkThrottle{}, fmt.Errorf("%s must be 1 or 0: %q", WorkThrottleIOEnv, value)
			}
		}
	}
	return throttle, nil
}

// throttleJobVariables are set to the core limit unless the caller already
// set a lower or equal positive value.
var throttleJobVariables = []string{
	"GOMAXPROCS",                 // Go runtime, and go build/test default for -p
	"CMAKE_BUILD_PARALLEL_LEVEL", // cmake --build
	"CARGO_BUILD_JOBS",           // cargo
	"RAYON_NUM_THREADS",          // Rust rayon
	"OMP_NUM_THREADS",            // OpenMP
	"UV_CONCURRENT_BUILDS",       // uv
	"npm_config_jobs",            // node-gyp
}

// Environment returns environment with the parallelism limit applied. It
// never raises a limit the caller set, and it does not change the input.
func (throttle WorkThrottle) Environment(environment []string) []string {
	if throttle.Cores <= 0 {
		return environment
	}
	limit := strconv.Itoa(throttle.Cores)
	out := make([]string, 0, len(environment)+len(throttleJobVariables)+3)
	present := map[string]bool{}
	makeFlags, goFlags := "", ""
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			out = append(out, entry)
			continue
		}
		switch name {
		case workThrottleAppliedEnv:
			continue
		case "MAKEFLAGS":
			makeFlags = value
			continue
		case "GOFLAGS":
			goFlags = value
			continue
		}
		for _, variable := range throttleJobVariables {
			if name != variable {
				continue
			}
			present[name] = true
			if current, err := strconv.Atoi(value); err != nil || current < 1 || current > throttle.Cores {
				entry = name + "=" + limit
			}
		}
		out = append(out, entry)
	}
	for _, variable := range throttleJobVariables {
		if !present[variable] {
			out = append(out, variable+"="+limit)
		}
	}
	out = append(out, "MAKEFLAGS="+throttledMakeFlags(makeFlags, throttle.Cores))
	out = append(out, "GOFLAGS="+throttledGoFlags(goFlags, throttle.Cores))
	return append(out, workThrottleAppliedEnv+"="+limit)
}

// throttledGoFlags sets -p, which bounds parallel compiles and parallel test
// binaries. GOMAXPROCS alone leaves `go test ./...` running one binary per
// logical CPU when the caller passed its own -p.
func throttledGoFlags(flags string, cores int) string {
	fields := strings.Fields(flags)
	out := fields[:0:0]
	for _, field := range fields {
		if value, ok := strings.CutPrefix(field, "-p="); ok {
			if current, err := strconv.Atoi(value); err == nil && current >= 1 && current <= cores {
				return flags
			}
			continue
		}
		out = append(out, field)
	}
	return strings.Join(append(out, "-p="+strconv.Itoa(cores)), " ")
}

// throttledMakeFlags sets -j for GNU make. An existing lower -jN is kept. A
// bare -j (unlimited) is replaced.
func throttledMakeFlags(flags string, cores int) string {
	fields := strings.Fields(flags)
	out := fields[:0:0]
	for _, field := range fields {
		if value, ok := strings.CutPrefix(field, "-j"); ok {
			if current, err := strconv.Atoi(value); err == nil && current >= 1 && current <= cores {
				return flags
			}
			continue
		}
		out = append(out, field)
	}
	return strings.Join(append(out, "-j"+strconv.Itoa(cores)), " ")
}

// IOWrapper returns the argv prefix that lowers disk I/O priority on goos,
// or nil when the host has no such tool. lookPath is exec.LookPath in
// production.
func (throttle WorkThrottle) IOWrapper(goos string, lookPath func(string) (string, error)) []string {
	if !throttle.BackgroundIO {
		return nil
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	switch goos {
	case "darwin":
		// taskpolicy -d throttle sets the disk I/O policy only. It leaves CPU
		// scheduling alone, unlike -b, which also moves the work to the
		// efficiency cores.
		if path, err := lookPath("taskpolicy"); err == nil {
			return []string{path, "-d", "throttle"}
		}
	case "linux":
		if path, err := lookPath("ionice"); err == nil {
			return []string{path, "-c", "3"}
		}
	}
	return nil
}

// Apply returns the executable, arguments and environment to start.
func (throttle WorkThrottle) Apply(executable string, args, environment []string, goos string, lookPath func(string) (string, error)) (string, []string, []string) {
	environment = throttle.Environment(environment)
	if wrapper := throttle.IOWrapper(goos, lookPath); len(wrapper) > 0 {
		wrapped := append(append([]string{}, wrapper[1:]...), executable)
		return wrapper[0], append(wrapped, args...), environment
	}
	return executable, args, environment
}
