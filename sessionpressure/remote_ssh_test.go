package sessionpressure

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteSSHControlDirAcceptsOnlyPrivateOwnedDirectory(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	if got, err := remoteSSHControlDir(private); err != nil || got != private {
		t.Fatalf("new private dir = %q, %v", got, err)
	}
	if info, _ := os.Stat(private); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", info.Mode().Perm())
	}
	shared := filepath.Join(base, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteSSHControlDir(shared); err == nil {
		t.Fatal("group-accessible dir accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteSSHControlDir(link); err == nil {
		t.Fatal("symlinked dir accepted")
	}
}

func TestRemoteSSHReusesOnePrivateConnectionPerJob(t *testing.T) {
	job := "0123456789abcdef0123456789abcdef"
	conn := newRemoteSSH(job)
	if conn.control == "" {
		t.Skip("no private /tmp control directory on this host")
	}
	if !strings.HasSuffix(conn.control, "/"+job[:16]+"-%C") {
		t.Fatalf("control path %q is not private to the job", conn.control)
	}
	cmd := conn.command(t.Context(), "8", "builder@runner-a", "true")
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=8", "ControlMaster=auto", "ControlPath=" + conn.control, "ControlPersist=" + remoteSSHPersist} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q lack %q", got, want)
		}
	}
	if cmd.Args[len(cmd.Args)-2] != "builder@runner-a" || cmd.Args[len(cmd.Args)-1] != "true" {
		t.Fatalf("target and command must come last: %q", cmd.Args)
	}
	if conn.probe() == nil {
		t.Fatal("reuse must hand the fleet probe its options")
	}
}

func TestRemoteSSHControlPathFitsTheBoundSocket(t *testing.T) {
	conn := newRemoteSSH("0123456789abcdef0123456789abcdef")
	if conn.control == "" {
		t.Skip("no private /tmp control directory on this host")
	}
	// The master binds <ControlPath with %C expanded>.<16 random chars>.
	bound := strings.Replace(conn.control, "%C", strings.Repeat("f", 40), 1) + "." + strings.Repeat("x", 16)
	if len(bound) > unixSocketPathMax {
		t.Fatalf("bound socket path is %d bytes, over %d: %s", len(bound), unixSocketPathMax, bound)
	}
	long := "/tmp/" + strings.Repeat("d", 40) + "/0123456789abcdef-%C"
	if remoteSSHControlFits(long) {
		t.Fatalf("%s fits, but its bound socket is %d bytes", long, len(long)-2+40+17)
	}
}

func TestRemoteSSHOptOutKeepsSeparateConnections(t *testing.T) {
	t.Setenv(remoteSSHMuxEnv, "off")
	conn := newRemoteSSH("0123456789abcdef0123456789abcdef")
	if conn.control != "" || conn.options() != nil || conn.probe() != nil {
		t.Fatalf("opt-out still reuses: %+v", conn)
	}
	got := strings.Join(conn.command(t.Context(), "5", "builder@runner-a", "true").Args, " ")
	if strings.Contains(got, "Control") {
		t.Fatalf("opt-out args %q carry control options", got)
	}
	conn.close("builder@runner-a") // no master: must not run ssh
}

// fetchStream returns a command that prints manifest and then body, as the
// remote `cat -- <manifest> <artifact>` does.
func fetchStream(t *testing.T, manifest string, body []byte) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	m, a := filepath.Join(dir, "m"), filepath.Join(dir, "a")
	if err := os.WriteFile(m, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command("cat", "--", m, a)
}

func TestFetchRemoteArtifactReadsManifestAndArtifactFromOneStream(t *testing.T) {
	body := bytes.Repeat([]byte("artifact\n\x00\xff"), 40000) // spans several reader buffers
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	const path = "/srv/build/candidates/pressure-x/artifact"
	var out bytes.Buffer
	got, err := fetchRemoteArtifact(fetchStream(t, want+"  "+path+"\n", body), path, &out)
	if err != nil || got != want {
		t.Fatalf("fetch = %q, %v", got, err)
	}
	if !bytes.Equal(out.Bytes(), body) {
		t.Fatalf("artifact bytes differ: %d vs %d", out.Len(), len(body))
	}
}

func TestFetchRemoteArtifactRejectsBadManifestOrDigest(t *testing.T) {
	body := []byte("binary")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	const path = "/srv/build/candidates/pressure-x/artifact"
	cases := map[string]string{
		"wrong digest":   strings.Repeat("0", 64) + "  " + path + "\n",
		"wrong path":     good + "  /srv/build/candidates/other/artifact\n",
		"upper hex":      strings.ToUpper(good) + "  " + path + "\n",
		"short digest":   good[:63] + "  " + path + "\n",
		"no manifest":    "",
		"extra field":    good + "  " + path + " x\n",
		"unterminated":   good + "  " + path,
		"manifest only?": good + "\n",
	}
	for name, manifest := range cases {
		if _, err := fetchRemoteArtifact(fetchStream(t, manifest, body), path, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: fetch accepted", name)
		}
	}
	// A failing remote command fails the fetch even with a valid stream.
	failing := exec.Command("sh", "-c", "printf '%s  %s\\n' \"$1\" \"$2\"; printf binary; exit 1", "sh", good, path)
	if _, err := fetchRemoteArtifact(failing, path, &bytes.Buffer{}); err == nil {
		t.Error("nonzero exit accepted")
	}
}
