package sessionpressure

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Remote source delta transport.
//
// Measured 2026-11-09 (24 h, 129 remote jobs with phase timings): the
// gzip tar of the whole module closure took as long as the runner itself
// (source upload 58 min, wait and run 57 min; upload p50 11 s, p90 41 s,
// about 5 to 11 MB/s for 100 to 170 MB closures). Two uploads of the same
// closure usually differ in a few files.
//
// The box keeps one pristine base tree per closure key under
// remoteSourceBaseRoot, with a manifest of every file's hash and mode. The
// Mac sends only the files whose content or mode differ from a base it
// knows, a removal list, and the complete new manifest. Under the existing
// host-wide transfer lock the box checks that its base is the one the Mac
// diffed against (exit 45 when it is not), applies the change, verifies
// every byte of the base against the new manifest (exit 46 drops the base),
// and copies the base into the job's stage. The stage is a copy, never a
// hard link: a later upload changes the base while the runner may still be
// snapshotting an earlier stage.
//
// The manifest is built from the bytes actually sent, so a file edited
// during the upload yields a consistent (slightly older or newer) snapshot,
// never a manifest that disagrees with the tree.
//
// SESSION_PRESSURE_REMOTE_DELTA=off restores the full tar upload.

// The box paths are variables so tests can run the real script in a
// temporary directory.
var (
	remoteSourceBaseRoot     = "/srv/build/cache/pressure-base"
	remoteSourceTransferLock = "/srv/build/input-transfer.lock"
)

const (
	remoteSourceDeltaEnv   = "SESSION_PRESSURE_REMOTE_DELTA"
	remoteSourceBaseKeep   = 8
	remoteSourceExitStale  = 45
	remoteSourceExitBroken = 46
	// A delta that resends more than this share of files is a full seed.
	remoteSourceDeltaMaxShare = 0.5
	remoteSourceControlDir    = ".ncd-pressure-delta"
	remoteSourceTreeDir       = "tree"
)

// Upload modes recorded as source_upload_mode.
const (
	remoteUploadFull        = "full"
	remoteUploadSeed        = "seed"
	remoteUploadDeltaKnown  = "delta-known"
	remoteUploadDeltaListed = "delta-listed"
)

var errRemoteSourceStale = errors.New("remote source base is not the expected base")

// errRemoteSourceUnsupported means the closure holds a path the manifest
// cannot carry; the caller uses the full tar upload instead.
var errRemoteSourceUnsupported = errors.New("delta transport cannot carry this closure")

// remoteSourceEntry is one closure file.
type remoteSourceEntry struct {
	rel   string
	path  string
	mode  fs.FileMode
	size  int64
	mtime int64 // UnixNano
	ino   uint64
	hash  string
}

// remoteSourceLine is one manifest line: content hash and permission bits.
type remoteSourceLine struct {
	hash string
	mode fs.FileMode
}

// remoteSourceUpload reports what one upload sent.
type remoteSourceUpload struct {
	mode  string
	files int
	bytes int64
	// Step timings: listing the closure, hashing it (stat-cache misses
	// only), and every send attempt including box-side work.
	collectMS, hashMS, sendMS int64
	hashed                    int
}

func remoteSourceDeltaEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(remoteSourceDeltaEnv)), "off")
}

// safeRemoteSourceRel reports whether rel can travel through the manifest
// and sha256sum unchanged: relative, clean, no newline or backslash.
func safeRemoteSourceRel(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\n\r\\\x00") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, " ") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return !strings.HasPrefix(rel, remoteSourceControlDir+"/")
}

// collectRemoteSources lists the closure exactly as the full upload does.
func collectRemoteSources(modules []string, common string, scope remoteInputScope) ([]remoteSourceEntry, int64, error) {
	var entries []remoteSourceEntry
	var total int64
	err := walkRemoteSources(modules, common, scope, func(rel, path string, info os.FileInfo) error {
		total += info.Size()
		if total > remoteSourceLimit {
			return errors.New("remote source exceeds 512MiB; narrow the module")
		}
		entries = append(entries, remoteSourceEntry{rel: filepath.ToSlash(rel), path: path, mode: info.Mode().Perm(), size: info.Size(), mtime: info.ModTime().UnixNano(), ino: remoteSourceInode(info)})
		return nil
	})
	sort.Slice(entries, func(a, b int) bool { return entries[a].rel < entries[b].rel })
	return entries, total, err
}

func hashRemoteSourceFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// remoteSourceStat is one stat-cache entry: a file whose size, mtime and
// inode are unchanged keeps its hash without being read again.
type remoteSourceStat struct {
	Size  int64  `json:"s"`
	Mtime int64  `json:"m"`
	Ino   uint64 `json:"i"`
	Hash  string `json:"h"`
}

// remoteSourceRacyWindow: a file modified this close to the scan may change
// again within the same mtime, so its cached hash is never trusted or kept.
const remoteSourceRacyWindow = 2 * time.Second

// hashRemoteSources fills each entry's hash, reading only files the stat
// cache cannot vouch for, and returns the updated cache and the number of
// files read. A file removed since the walk keeps an empty hash and is
// treated as changed, so the send step drops it.
func hashRemoteSources(entries []remoteSourceEntry, cache map[string]remoteSourceStat, scanStart time.Time) (map[string]remoteSourceStat, int, error) {
	next := make(map[string]remoteSourceStat, len(entries))
	racy := scanStart.Add(-remoteSourceRacyWindow).UnixNano()
	read := 0
	for i := range entries {
		e := &entries[i]
		if c, ok := cache[e.rel]; ok && e.mtime < racy && c.Size == e.size && c.Mtime == e.mtime && c.Ino == e.ino && len(c.Hash) == 64 {
			e.hash = c.Hash
			next[e.rel] = c
			continue
		}
		hash, err := hashRemoteSourceFile(e.path)
		read++
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, read, err
		}
		e.hash = hash
		if e.mtime < racy {
			next[e.rel] = remoteSourceStat{Size: e.size, Mtime: e.mtime, Ino: e.ino, Hash: hash}
		}
	}
	return next, read, nil
}

func remoteSourceStatPath(stateDir, key string) string {
	return filepath.Join(stateDir, "remote-source-bases", key+".stat.json")
}

func loadRemoteSourceStat(stateDir, key string) map[string]remoteSourceStat {
	body, err := os.ReadFile(remoteSourceStatPath(stateDir, key))
	if err != nil {
		return nil
	}
	var cache map[string]remoteSourceStat
	if json.Unmarshal(body, &cache) != nil {
		return nil
	}
	return cache
}

func saveRemoteSourceStat(stateDir, key string, cache map[string]remoteSourceStat) {
	body, err := json.Marshal(cache)
	if err != nil {
		return
	}
	path := remoteSourceStatPath(stateDir, key)
	if os.MkdirAll(filepath.Dir(path), 0700) == nil {
		_ = atomicWrite(path, body, 0600)
	}
}

// renderRemoteSourceManifest writes "<sha256> <mode> <path>" lines with a
// fixed-width mode, so the box can take the path from column 71.
func renderRemoteSourceManifest(lines map[string]remoteSourceLine) []byte {
	names := make([]string, 0, len(lines))
	for name := range lines {
		names = append(names, name)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&b, "%s %04o %s\n", lines[name].hash, lines[name].mode&0o7777, name)
	}
	return b.Bytes()
}

func parseRemoteSourceManifest(r io.Reader) (map[string]remoteSourceLine, error) {
	out := map[string]remoteSourceLine{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 71 || line[64] != ' ' || line[69] != ' ' {
			return nil, fmt.Errorf("malformed source manifest line %q", boundedRemoteLine(line))
		}
		hash, modeText, name := line[:64], line[65:69], line[70:]
		if _, err := hex.DecodeString(hash); err != nil || !safeRemoteSourceRel(name) {
			return nil, fmt.Errorf("malformed source manifest line %q", boundedRemoteLine(line))
		}
		var mode uint32
		if _, err := fmt.Sscanf(modeText, "%04o", &mode); err != nil {
			return nil, fmt.Errorf("malformed source manifest mode %q", modeText)
		}
		out[name] = remoteSourceLine{hash: hash, mode: fs.FileMode(mode)}
	}
	return out, scanner.Err()
}

func boundedRemoteLine(line string) string {
	if len(line) > 120 {
		return line[:120]
	}
	return line
}

func remoteSourceDigest(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return hex.EncodeToString(sum[:])
}

// planRemoteSourceDelta returns the entries to send and the base paths to
// remove. A nil base sends everything.
func planRemoteSourceDelta(entries []remoteSourceEntry, base map[string]remoteSourceLine) (send []int, removed []string) {
	present := make(map[string]bool, len(entries))
	for i, e := range entries {
		present[e.rel] = true
		line, ok := base[e.rel]
		if base == nil || !ok || e.hash == "" || line.hash != e.hash || line.mode != e.mode {
			send = append(send, i)
		}
	}
	for name := range base {
		if !present[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return send, removed
}

// writeRemoteSourceDelta streams the gzip tar: tree/<rel> for every sent
// file, then the control files. It returns the new manifest and the bytes
// sent. A file that vanished before it was read is dropped from the
// manifest; a file that changed after hashing is sent as read.
func writeRemoteSourceDelta(w io.Writer, entries []remoteSourceEntry, send []int, removed []string, base map[string]remoteSourceLine, expect string) ([]byte, int64, error) {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	fail := func(err error) ([]byte, int64, error) {
		_ = tw.Close()
		_ = zw.Close()
		return nil, 0, err
	}
	lines := make(map[string]remoteSourceLine, len(entries))
	sending := make(map[int]bool, len(send))
	for _, i := range send {
		sending[i] = true
	}
	for i, e := range entries {
		if !sending[i] {
			lines[e.rel] = base[e.rel]
		}
	}
	now := time.Now()
	var sent int64
	for _, i := range send {
		e := entries[i]
		body, err := os.ReadFile(e.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		if int64(len(body)) > 20<<20 {
			continue
		}
		info, err := os.Lstat(e.path)
		if err != nil {
			return fail(err)
		}
		mode := info.Mode().Perm()
		sum := sha256.Sum256(body)
		lines[e.rel] = remoteSourceLine{hash: hex.EncodeToString(sum[:]), mode: mode}
		if err := tw.WriteHeader(&tar.Header{Name: remoteSourceTreeDir + "/" + e.rel, Mode: int64(mode), Size: int64(len(body)), ModTime: info.ModTime(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return fail(err)
		}
		if _, err := tw.Write(body); err != nil {
			return fail(err)
		}
		sent += int64(len(body))
	}
	manifest := renderRemoteSourceManifest(lines)
	var removal bytes.Buffer
	for _, name := range removed {
		removal.WriteString(name)
		removal.WriteByte(0)
	}
	control := []struct {
		name string
		body []byte
	}{
		{"manifest", manifest},
		{"digest", []byte(remoteSourceDigest(manifest) + "\n")},
		{"expect", []byte(expect + "\n")},
		{"removed", removal.Bytes()},
	}
	for _, c := range control {
		if err := tw.WriteHeader(&tar.Header{Name: remoteSourceControlDir + "/" + c.name, Mode: 0600, Size: int64(len(c.body)), ModTime: now, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return fail(err)
		}
		if _, err := tw.Write(c.body); err != nil {
			return fail(err)
		}
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		return nil, 0, err
	}
	return manifest, sent, nil
}

// remoteSourceDeltaScript builds the stage on the box.
//
// Outside the host-wide transfer lock (a slow sender holds no one up): the
// stream is extracted and every incoming file is checked against the new
// manifest. Under the lock: the base digest is compared with the one the Mac
// diffed against (exit 45), the base's manifest and digest are removed
// before any change so an interrupted apply forces a reseed, the change is
// applied, the file count is checked (exit 46 drops the base), the manifest
// and digest are written last, and the stage is made as a hard-link snapshot.
// After the lock is released every byte of the stage is checked against the
// manifest (exit 46 drops a damaged base).
//
// The snapshot is safe because the base is never written in place: changed
// files replace the old inode (cp --remove-destination), removals unlink,
// and a seed renames a new tree into place. An earlier stage keeps its own
// inodes, and the runner only reads the stage (it copies it into retained
// inputs before execution).
func remoteSourceDeltaScript(remote, key, request string) string {
	base := remoteSourceBaseRoot + "/" + key
	tree := "\"$inc/" + remoteSourceTreeDir + "\""
	return strings.Join([]string{
		"set -eu",
		"umask 077",
		"remote=" + shellQuote(remote),
		"base=" + shellQuote(base),
		"inc=\"$remote/.incoming\"",
		"mkdir -p \"$inc\" " + shellQuote(remoteSourceBaseRoot),
		"printf '%s\\n' " + shellQuote(request) + " > \"$remote.request\"",
		"tar -xzf - -C \"$inc\"",
		"ctl=\"$inc/" + remoteSourceControlDir + "\"",
		"test \"$(sha256sum < \"$ctl/manifest\" | cut -c1-64)\" = \"$(cut -c1-64 \"$ctl/digest\")\"",
		// Removal entries come from a manifest the box accepted; refuse any
		// path that could leave the base anyway.
		"if tr '\\000' '\\n' < \"$ctl/removed\" | grep -qE '(^|/)\\.\\.(/|$)|^/'; then exit 2; fi",
		// Every sent file must be in the manifest with the same content.
		"if [ -d " + tree + " ]; then",
		"  (cd " + tree + " && find . -type f -printf '%P\\n') | awk 'NR==FNR{h[substr($0,71)]=substr($0,1,64); next} !($0 in h){exit 3} {print h[$0] \"  \" $0}' \"$ctl/manifest\" - > \"$ctl/check\"",
		"  (cd " + tree + " && sha256sum --quiet --strict -c \"$ctl/check\" >/dev/null)",
		"fi",
		"exec 9>" + shellQuote(remoteSourceTransferLock),
		"flock -w 600 9",
		"expect=$(cat \"$ctl/expect\")",
		"drop() { rm -rf \"$base\" \"$base.manifest\" \"$base.digest\" \"$base.new\"; }",
		"if [ \"$expect\" = none ]; then",
		"  drop",
		"  if [ -d " + tree + " ]; then mv " + tree + " \"$base.new\"; else mkdir \"$base.new\"; fi",
		"  mv \"$base.new\" \"$base\"",
		"else",
		"  if [ ! -d \"$base\" ] || [ \"$(cat \"$base.digest\" 2>/dev/null)\" != \"$expect\" ]; then rm -rf \"$inc\"; exit " + fmt.Sprint(remoteSourceExitStale) + "; fi",
		"  rm -f \"$base.digest\" \"$base.manifest\"",
		"  if [ -d " + tree + " ]; then cp -a --remove-destination " + strings.TrimSuffix(tree, "\"") + "/.\" \"$base/\"; fi",
		"  if [ -s \"$ctl/removed\" ]; then (cd \"$base\" && xargs -0 rm -f -- < \"$ctl/removed\"); fi",
		"  find \"$base\" -mindepth 1 -type d -empty -delete",
		"fi",
		// Nothing beyond the manifest: every listed file was checked when it
		// arrived, and the count rules out strays.
		"if [ \"$(find \"$base\" -type f | wc -l)\" -ne \"$(wc -l < \"$ctl/manifest\")\" ]; then drop; rm -rf \"$inc\"; exit " + fmt.Sprint(remoteSourceExitBroken) + "; fi",
		"cp \"$ctl/manifest\" \"$base.manifest\"",
		"cut -c1-64 \"$ctl/digest\" > \"$base.digest\"",
		"rm -rf \"$remote/source\"",
		"cp -al \"$base\" \"$remote/source\"",
		// Release the lock, then check every byte of the stage. It shares
		// inodes with the base, so this also catches a base damaged on disk;
		// a damaged base is dropped (if still current) and the Mac reseeds.
		"exec 9>&-",
		"if ! (cd \"$remote/source\" && awk '{print substr($0,1,64) \"  \" substr($0,71)}' \"$ctl/manifest\" | sha256sum --quiet --strict -c - >/dev/null 2>&1); then",
		"  exec 9>" + shellQuote(remoteSourceTransferLock),
		"  flock -w 600 9",
		"  if [ \"$(cat \"$base.digest\" 2>/dev/null)\" = \"$(cut -c1-64 \"$ctl/digest\")\" ]; then drop; fi",
		"  rm -rf \"$remote/source\" \"$inc\"",
		"  exit " + fmt.Sprint(remoteSourceExitBroken),
		"fi",
		"rm -rf \"$inc\"",
		// Keep the newest bases; each is one closure (100 to 400 MB).
		"ls -1t " + shellQuote(remoteSourceBaseRoot) + "/*.digest 2>/dev/null | tail -n +" + fmt.Sprint(remoteSourceBaseKeep+1) + " | while read -r old; do b=${old%.digest}; rm -rf \"$b\" \"$b.manifest\" \"$b.digest\"; done",
	}, "\n")
}

// remoteSourceBaseKey names one closure on one box. It depends only on the
// closure layout, so every caller of the same checkout shares the base.
func remoteSourceBaseKey(host, common string, modules []string, scope remoteInputScope) string {
	sorted := append([]string(nil), modules...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(host + "\x00" + common + "\x00" + fmt.Sprint(int(scope)) + "\x00" + strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// localRemoteSourceBase is the manifest the Mac last sent for key.
func localRemoteSourceBasePath(stateDir, key string) string {
	return filepath.Join(stateDir, "remote-source-bases", key+".manifest")
}

func loadLocalRemoteSourceBase(stateDir, key string) (map[string]remoteSourceLine, string, bool) {
	body, err := os.ReadFile(localRemoteSourceBasePath(stateDir, key))
	if err != nil {
		return nil, "", false
	}
	lines, err := parseRemoteSourceManifest(bytes.NewReader(body))
	if err != nil {
		return nil, "", false
	}
	return lines, remoteSourceDigest(body), true
}

func saveLocalRemoteSourceBase(stateDir, key string, manifest []byte) {
	path := localRemoteSourceBasePath(stateDir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = atomicWrite(path, manifest, 0600)
}

// uploadRemoteSourcesDelta builds remote/source on the box. ssh runs one
// remote command. It tries the base this Mac last sent, then the base the
// box reports, then a full seed; any other failure is returned.
func uploadRemoteSourcesDelta(ssh func(string) *exec.Cmd, stderr io.Writer, stateDir, host, remote, request, common string, modules []string, scope remoteInputScope) (remoteSourceUpload, error) {
	scanStart := time.Now()
	entries, _, err := collectRemoteSources(modules, common, scope)
	collectMS := time.Since(scanStart).Milliseconds()
	if err != nil {
		return remoteSourceUpload{}, err
	}
	for _, e := range entries {
		if !safeRemoteSourceRel(e.rel) {
			return remoteSourceUpload{}, fmt.Errorf("%w: %q", errRemoteSourceUnsupported, boundedRemoteLine(e.rel))
		}
	}
	key := remoteSourceBaseKey(host, common, modules, scope)
	hashStarted := time.Now()
	stat, hashed, err := hashRemoteSources(entries, loadRemoteSourceStat(stateDir, key), scanStart)
	if err != nil {
		return remoteSourceUpload{}, err
	}
	hashMS := time.Since(hashStarted).Milliseconds()
	saveRemoteSourceStat(stateDir, key, stat)
	sendStarted := time.Now()
	send := func(mode string, base map[string]remoteSourceLine, expect string) (remoteSourceUpload, error) {
		plan, removed := planRemoteSourceDelta(entries, base)
		if base != nil && float64(len(plan)) > remoteSourceDeltaMaxShare*float64(max(len(entries), 1)) {
			mode, base, expect, removed = remoteUploadSeed, nil, "none", nil
			plan, _ = planRemoteSourceDelta(entries, nil)
		}
		cmd := ssh("sh -c " + shellQuote(remoteSourceDeltaScript(remote, key, request)))
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return remoteSourceUpload{}, err
		}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return remoteSourceUpload{}, err
		}
		manifest, sent, writeErr := writeRemoteSourceDelta(pipe, entries, plan, removed, base, expect)
		_ = pipe.Close()
		waitErr := cmd.Wait()
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) && (exit.ExitCode() == remoteSourceExitStale || exit.ExitCode() == remoteSourceExitBroken) {
			return remoteSourceUpload{mode: mode}, errRemoteSourceStale
		}
		if err := errors.Join(writeErr, waitErr); err != nil {
			return remoteSourceUpload{}, err
		}
		saveLocalRemoteSourceBase(stateDir, key, manifest)
		return remoteSourceUpload{mode: mode, files: len(plan), bytes: sent, collectMS: collectMS, hashMS: hashMS, hashed: hashed, sendMS: time.Since(sendStarted).Milliseconds()}, nil
	}
	if base, digest, ok := loadLocalRemoteSourceBase(stateDir, key); ok {
		up, err := send(remoteUploadDeltaKnown, base, digest)
		if !errors.Is(err, errRemoteSourceStale) {
			return up, err
		}
	}
	// Another caller moved the base, or this Mac never sent one: diff
	// against what the box holds.
	if body, err := ssh("cat " + shellQuote(remoteSourceBaseRoot+"/"+key+".manifest") + " 2>/dev/null").Output(); err == nil && len(body) > 0 {
		if base, parseErr := parseRemoteSourceManifest(bytes.NewReader(body)); parseErr == nil {
			up, err := send(remoteUploadDeltaListed, base, remoteSourceDigest(body))
			if !errors.Is(err, errRemoteSourceStale) {
				return up, err
			}
		}
	}
	up, err := send(remoteUploadSeed, nil, "none")
	if errors.Is(err, errRemoteSourceStale) {
		return up, errors.New("remote source seed failed verification")
	}
	return up, err
}
