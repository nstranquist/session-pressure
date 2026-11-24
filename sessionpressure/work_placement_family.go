package sessionpressure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// The exact placement key (cwd + argv) hardly repeats: on 2026-11-15 only 3
// of 23 keys had the two successful remote samples the placer needs, because
// agents vary test selection flags and run from temporary checkouts. The
// family key is the coarse fallback. It names the project by its declared
// identity (Go module path, package.json name, Swift package directory name
// or git root name) instead of its absolute path, keeps the cwd relative to
// that root, and drops test-selection and verbosity flags. Package arguments
// are kept, so `go test ./a` and `go test ./...` stay apart.
//
// The placer uses family history only when the exact key has too few
// samples on either side, and records which scope decided.

// PlacementKeys carries both digests of one command.
type PlacementKeys struct {
	Exact  string
	Family string
}

func NewPlacementKeys(cwd string, argv []string) PlacementKeys {
	return PlacementKeys{Exact: PlacementKey(cwd, argv), Family: PlacementFamilyKey(cwd, argv)}
}

// selectionFlags change which tests run or how they print, not what is built.
// The value counts say whether the flag takes the next argument when it is
// not written as -flag=value.
var selectionFlags = map[string]bool{
	"-run": true, "-skip": true, "-count": true, "-timeout": true, "-shuffle": true,
	"-o": true, "-coverprofile": true, "-cpuprofile": true, "-memprofile": true,
	"-blockprofile": true, "-mutexprofile": true, "-trace": true,
	"-v": false, "-failfast": false, "-json": false,
	"-t": true, "--testNamePattern": true, "--grep": true, "--reporter": true,
	"--verbose": false, "--silent": false,
}

func PlacementFamilyKey(cwd string, argv []string) string {
	cwd = filepath.Clean(cwd)
	root, identity := projectIdentity(cwd)
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		rel = cwd
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "family-v1\x00"+identity+"\x00"+filepath.ToSlash(rel))
	for _, arg := range normalizeFamilyArgv(root, argv) {
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, arg)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func normalizeFamilyArgv(root string, argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := []string{filepath.Base(argv[0])}
	var positional []string
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		name, _, hasValue := strings.Cut(arg, "=")
		if takesValue, ok := selectionFlags[name]; ok {
			if takesValue && !hasValue && i+1 < len(argv) {
				i++
			}
			continue
		}
		if filepath.IsAbs(arg) {
			if rel, err := filepath.Rel(root, arg); err == nil && !strings.HasPrefix(rel, "..") {
				arg = "./" + filepath.ToSlash(rel)
			}
		}
		if strings.HasPrefix(arg, "-") || len(out) < 2 {
			// Flags and the subcommand keep their place.
			out = append(out, arg)
			continue
		}
		positional = append(positional, arg)
	}
	// Package and path arguments are a set, not a sequence.
	slices.Sort(positional)
	return append(out, positional...)
}

// projectIdentity walks up from cwd to the nearest project root and names it
// by what the project declares. Without a root, the cwd is its own identity
// and the family key is only as coarse as the argv normalisation.
func projectIdentity(cwd string) (string, string) {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if body, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if path := modfile.ModulePath(body); path != "" {
				return dir, "go:" + path
			}
		}
		if body, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(body, &pkg) == nil && pkg.Name != "" {
				return dir, "node:" + pkg.Name
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "Package.swift")); err == nil {
			return dir, "swift:" + filepath.Base(dir)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, "git:" + filepath.Base(dir)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return cwd, "path:" + cwd
		}
	}
}
