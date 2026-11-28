// Package frontdoor records the host wrapper ("front door") that execs the
// session-pressure CLI and also links the session-pressure library into its
// own binary.
//
// A front door is promoted by its own build, not by session-pressure's
// release. After a session-pressure promotion it still runs the old library
// code until it is rebuilt. The wrapper names itself through Env on every
// call it forwards. The CLI records that in the state directory, so a later
// promotion can rebuild the front door and check the doctor through it, even
// when nothing wrapped the promotion itself.
//
// This package is the contract for both sides. It has no dependency on the
// rest of the module, so a wrapper can import it cheaply.
package frontdoor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	// Env carries the wrapper's Registration as JSON (EnvValue).
	Env = "SESSION_PRESSURE_FRONT_DOOR"
	// RevisionEnv carries the session-pressure revision linked into the
	// wrapper that execed the CLI.
	RevisionEnv = "SESSION_PRESSURE_FRONT_DOOR_REVISION"
	// MarkerEnv marks a process the wrapper execed. A wrapper refuses to
	// run under it, to stop a forwarding loop.
	MarkerEnv = "NDEV_PRESSURE_WRAPPER"
	// PinEnv carries the exact 40-hex session-pressure commit that a
	// Republish command must link. Promotion sets it so the front door
	// links the promoted revision even when the sibling checkout's HEAD
	// moves during the build. A Republish command that cannot link that
	// commit must fail, not link another one.
	PinEnv = "SESSION_PRESSURE_FRONT_DOOR_PIN"
	// FileName is the recorded registration in the state directory.
	FileName = "front-door.json"

	maxNameBytes = 64
	maxArgs      = 32
	maxArgBytes  = 4096
)

// Registration names a front door, how to call the session-pressure CLI
// through it, and how to rebuild it.
type Registration struct {
	// Name is a short label for reports, for example "ndev".
	Name string `json:"name"`
	// Invoke is the argv prefix that reaches the CLI through the front door.
	// Invoke[0] is an absolute path. Promotion appends "--json doctor".
	Invoke []string `json:"invoke"`
	// Republish is the argv that rebuilds and installs the front door from
	// committed source. It runs without a shell.
	Republish []string `json:"republish"`
	// Revision is the session-pressure revision the front door linked when
	// it last forwarded a call. The CLI fills it from the wrapper's
	// revision env; it is history, not proof of what is installed now.
	Revision string `json:"revision,omitempty"`
}

// Validate rejects a registration that promotion could not run safely.
func (r Registration) Validate() error {
	name := strings.TrimSpace(r.Name)
	switch {
	case name == "" || len(name) > maxNameBytes:
		return fmt.Errorf("front door name must be 1-%d bytes", maxNameBytes)
	case len(r.Invoke) == 0 || !filepath.IsAbs(r.Invoke[0]):
		return errors.New("front door invoke must start with an absolute executable path")
	case len(r.Republish) == 0 || strings.TrimSpace(r.Republish[0]) == "":
		return errors.New("front door republish command is empty")
	}
	for _, argv := range [][]string{r.Invoke, r.Republish} {
		if len(argv) > maxArgs {
			return fmt.Errorf("front door command has more than %d arguments", maxArgs)
		}
		for _, arg := range argv {
			if len(arg) > maxArgBytes || strings.ContainsRune(arg, 0) {
				return errors.New("front door command has an invalid argument")
			}
		}
	}
	return nil
}

// EnvValue encodes the registration for Env.
func (r Registration) EnvValue() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Parse decodes and validates an Env value.
func Parse(value string) (Registration, error) {
	var r Registration
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Registration{}, fmt.Errorf("decode %s: %w", Env, err)
	}
	r.Name = strings.TrimSpace(r.Name)
	r.Revision = strings.TrimSpace(r.Revision)
	if err := r.Validate(); err != nil {
		return Registration{}, err
	}
	return r, nil
}

// WithoutWrapperEnv drops the variables a wrapper sets for the CLI it
// execs. A process that starts a front door or the CLI on its own, such as a
// promotion, uses it: an inherited front-door revision describes whatever
// wrapped that process, not what is installed, and the marker makes a front
// door refuse to run.
func WithoutWrapperEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, item := range environ {
		switch name, _, _ := strings.Cut(item, "="); name {
		case Env, RevisionEnv, MarkerEnv, PinEnv:
			continue
		}
		out = append(out, item)
	}
	return out
}

// Path is the registration file in dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads the recorded registration. found is false when none exists.
func Load(dir string) (r Registration, found bool, err error) {
	raw, err := os.ReadFile(Path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Registration{}, false, nil
	}
	if err != nil {
		return Registration{}, false, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Registration{}, false, fmt.Errorf("decode %s: %w", Path(dir), err)
	}
	if err := r.Validate(); err != nil {
		return Registration{}, false, fmt.Errorf("%s: %w", Path(dir), err)
	}
	return r, true, nil
}

// Record stores r in dir unless the same registration is already there, so
// the common case is one small read and no write. It reports whether it
// wrote.
func Record(dir string, r Registration) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	if current, found, err := Load(dir); err == nil && found && reflect.DeepEqual(current, r) {
		return false, nil
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return false, err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".front-door-*.json")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), Path(dir)); err != nil {
		return false, err
	}
	return true, nil
}

// Status is the doctor's view of the recorded front door.
type Status struct {
	Registration
	Path          string `json:"path"`
	BinaryPresent bool   `json:"binary_present"`
	Detail        string `json:"detail,omitempty"`
}

// Inspect reports the recorded front door, or nil when none is recorded. A
// registration that cannot be read is reported with Detail, not as an error,
// because doctor must not fail on it.
func Inspect(dir string) *Status {
	r, found, err := Load(dir)
	if err != nil {
		return &Status{Path: Path(dir), Detail: err.Error()}
	}
	if !found {
		return nil
	}
	status := &Status{Registration: r, Path: Path(dir)}
	if info, err := os.Stat(r.Invoke[0]); err == nil && !info.IsDir() {
		status.BinaryPresent = true
	} else {
		status.Detail = "the front door executable is gone; delete " + Path(dir) + " if it was removed on purpose"
	}
	return status
}
