package sessionpressure

// Go -overlay and go env file inputs of express successful-result reuse.
//
// go build, test and vet compile the file that -overlay names instead of the
// file on disk. go list -overlay reports those files by their logical paths,
// so the source digest must hash the replacement, not the file at the path go
// list printed. Before this, a rerun with the same overlay JSON path and new
// replacement contents replayed the earlier pass, and a file that existed only
// in the overlay made the snapshot fail.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// expressGoOverlay is the effective -overlay of one Go command.
type expressGoOverlay struct {
	// digest is the sha256 of the overlay JSON, "" when the command has none.
	digest string
	size   int64
	// replace maps a canonical replaced path to its absolute replacement.
	// An empty replacement deletes the file.
	replace map[string]string
	dirs    map[string]string
}

// expressGoEnvFile is the go env file (go env GOENV) the command reads.
type expressGoEnvFile struct {
	digest string // "" when there is no file
	size   int64
	body   []byte
}

// readExpressGoEnvFile reads the go env file for environment. A missing file
// is not an error: the go command then uses its defaults.
func readExpressGoEnvFile(environment []string, workdir string) (expressGoEnvFile, error) {
	path := expressGoEnvFilePath(environment, workdir)
	if path == "" {
		return expressGoEnvFile{}, nil
	}
	body, info, err := readStableExpressInput(path)
	if errors.Is(err, fs.ErrNotExist) {
		return expressGoEnvFile{}, nil
	}
	if err != nil {
		return expressGoEnvFile{}, fmt.Errorf("read go env file: %w", err)
	}
	sum := sha256.Sum256(body)
	return expressGoEnvFile{digest: "sha256:" + hex.EncodeToString(sum[:]), size: info.Size(), body: body}, nil
}

// expressGoEnvFilePath follows the go command: GOENV names the file, GOENV=off
// disables it, and otherwise it is go/env under the user config directory.
func expressGoEnvFilePath(environment []string, workdir string) string {
	if file := environmentValue(environment, "GOENV"); file != "" {
		if file == "off" {
			return ""
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(workdir, file)
		}
		return filepath.Clean(file)
	}
	var dir string
	switch runtime.GOOS {
	case "darwin", "ios":
		home := environmentValue(environment, "HOME")
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, "Library", "Application Support")
	case "windows", "plan9":
		return ""
	default:
		dir = environmentValue(environment, "XDG_CONFIG_HOME")
		if dir == "" {
			home := environmentValue(environment, "HOME")
			if home == "" {
				return ""
			}
			dir = filepath.Join(home, ".config")
		} else if !filepath.IsAbs(dir) {
			return ""
		}
	}
	return filepath.Join(dir, "go", "env")
}

// goEnvFileValue returns the last value of key in a go env file body.
func goEnvFileValue(body []byte, key string) string {
	value := ""
	for len(body) > 0 {
		var line []byte
		line, body, _ = bytes.Cut(body, []byte("\n"))
		name, rest, ok := bytes.Cut(line, []byte("="))
		if ok && string(bytes.TrimSpace(name)) == key {
			value = strings.TrimSpace(string(rest))
		}
	}
	return value
}

// expressGoOverlayFlag returns the -overlay value the go command uses: the
// last -overlay on the command line, else the last one in GOFLAGS. GOFLAGS
// comes from the environment, or from the go env file when the environment
// leaves it empty, in the go command's own order.
func expressGoOverlayFlag(rest, environment []string, goEnv expressGoEnvFile) string {
	if value, ok := lastGoOverlayArg(rest, true); ok {
		return value
	}
	goflags := environmentValue(environment, "GOFLAGS")
	if strings.TrimSpace(goflags) == "" {
		goflags = goEnvFileValue(goEnv.body, "GOFLAGS")
	}
	value, _ := lastGoOverlayArg(strings.Fields(goflags), false)
	return value
}

// lastGoOverlayArg scans go flags for -overlay. On the command line a flag can
// take its value from the next argument, and flags end at -- or -args. GOFLAGS
// only allows -flag=value.
func lastGoOverlayArg(args []string, commandLine bool) (string, bool) {
	value, found := "", false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, inline, hasInline := goFlagName(arg)
		switch {
		case commandLine && name == "-args":
			return value, found
		case name == "-overlay" && hasInline:
			value, found = inline, true
		case name == "-overlay" && commandLine && i+1 < len(args):
			value, found = args[i+1], true
			i++
		case commandLine && goFlagsWithValue[name] && !hasInline:
			i++
		}
	}
	return value, found
}

// loadExpressGoOverlay reads the overlay JSON that value names. Relative
// paths, in the flag and in the JSON, are relative to the go command's
// working directory, as the go command reads them.
func loadExpressGoOverlay(value, workdir string) (expressGoOverlay, error) {
	if value == "" {
		return expressGoOverlay{}, nil
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	body, info, err := readStableExpressInput(filepath.Clean(path))
	if err != nil {
		return expressGoOverlay{}, fmt.Errorf("read go overlay: %w", err)
	}
	var parsed struct{ Replace map[string]string }
	if err := json.Unmarshal(body, &parsed); err != nil {
		return expressGoOverlay{}, fmt.Errorf("parse go overlay: %w", err)
	}
	if len(parsed.Replace) > workBatchMaximumEntries {
		return expressGoOverlay{}, fmt.Errorf("go overlay replaces more than %d files", workBatchMaximumEntries)
	}
	sum := sha256.Sum256(body)
	overlay := expressGoOverlay{
		digest:  "sha256:" + hex.EncodeToString(sum[:]),
		size:    info.Size(),
		replace: make(map[string]string, len(parsed.Replace)),
		dirs:    make(map[string]string),
	}
	for from, to := range parsed.Replace {
		if from == "" {
			return expressGoOverlay{}, errors.New("go overlay has an empty path key")
		}
		key := overlay.canonical(expressAbsolutePath(from, workdir))
		if _, duplicate := overlay.replace[key]; duplicate {
			return expressGoOverlay{}, fmt.Errorf("go overlay replaces %s twice", key)
		}
		if to != "" {
			to = expressAbsolutePath(to, workdir)
		}
		overlay.replace[key] = to
	}
	return overlay, nil
}

// lookup returns the replacement of a path go list printed.
func (o expressGoOverlay) lookup(path string) (replacement string, replaced bool) {
	if len(o.replace) == 0 {
		return "", false
	}
	replacement, replaced = o.replace[o.canonical(path)]
	return replacement, replaced
}

// canonical resolves symlinks in the directory part of an absolute path. go
// list reports directories under the real working directory, and an overlay
// key is joined to the caller's directory, which can reach the same place
// through a symlink (/tmp and /var on macOS). The directory need not exist: a
// package can exist only in the overlay.
func (o expressGoOverlay) canonical(path string) string {
	dir := filepath.Dir(path)
	resolved, ok := o.dirs[dir]
	if !ok {
		resolved = canonicalExpressDir(dir)
		o.dirs[dir] = resolved
	}
	return filepath.Join(resolved, filepath.Base(path))
}

func canonicalExpressDir(dir string) string {
	missing := ""
	for current := dir; ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, missing)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
		missing = filepath.Join(filepath.Base(current), missing)
	}
}

func expressAbsolutePath(path, workdir string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	return filepath.Clean(path)
}

// readStableExpressInput reads a small regular input file and fails when it
// changed while it was read.
func readStableExpressInput(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	if before.Size() > expressSourceListMaximumBytes {
		return nil, nil, fmt.Errorf("%s exceeds %d bytes", path, expressSourceListMaximumBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) != before.Size() || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return nil, nil, fmt.Errorf("%s changed while it was read", path)
	}
	return body, before, nil
}
