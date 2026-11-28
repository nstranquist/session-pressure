package sessionpressure

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

// Per-job SSH connection reuse. A remote job used to open a new
// SSH connection for each step: the fleet probe, the source upload, the
// runner subscription, the artifact manifest, the artifact and the cleanup.
// Each new login from the Mac costs about 1.7 s; a reused connection costs
// about 0.3 s, and a 6 MB fetch over it takes 1.4 s instead of 3 s.
//
// The first call of a job becomes the master (ControlMaster=auto), and
// OpenSSH moves it to the background with its stdio on /dev/null, so the
// caller's pipes close normally. Later calls of the same job reuse it.
// This is not the shared master that the gate-runner rule forbids on the
// gaterunner-N aliases: the socket is private to one job, so jobs never
// compete for sessions, and a dropped master fails only that job's calls.
// The next call then opens a new master, and the runner subscription
// reconnects to the same durable job as before. ControlPersist bounds an
// abandoned master (a killed caller) to remoteSSHPersist of idle time.
const (
	remoteSSHMuxEnv  = "SESSION_PRESSURE_SSH_MUX"
	remoteSSHPersist = "60"
	// unixSocketPathMax is the longest socket path that fits the 104-byte
	// sun_path of macOS with its terminating NUL.
	unixSocketPathMax = 103
	// sshControlHashLen is the length that %C expands to (a SHA-1 in hex).
	sshControlHashLen = 40
	// sshControlTempSuffixLen is the ".XXXXXXXXXXXXXXXX" suffix that the
	// master binds before it renames the socket into place, so the bound
	// path is 17 bytes longer than the ControlPath.
	sshControlTempSuffixLen = 17
)

// remoteSSH builds the ssh commands of one remote job.
type remoteSSH struct {
	// control is the ControlPath template; empty disables reuse.
	control string
}

// newRemoteSSH returns the job's ssh builder. Reuse is off when
// SESSION_PRESSURE_SSH_MUX=off or when no private socket directory is
// available; every call then opens its own connection, as before.
func newRemoteSSH(job string) remoteSSH {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(remoteSSHMuxEnv)), "off") || len(job) < 16 {
		return remoteSSH{}
	}
	dir, err := remoteSSHControlDir(filepath.Join("/tmp", fmt.Sprintf("sp-ssh-%d", os.Getuid())))
	if err != nil {
		return remoteSSH{}
	}
	control := filepath.Join(dir, job[:16]+"-%C")
	if !remoteSSHControlFits(control) {
		return remoteSSH{}
	}
	return remoteSSH{control: control}
}

// remoteSSHControlFits reports whether the socket that ssh binds for the
// control template fits a Unix socket address. ssh fails with exit 255
// ("too long for Unix domain socket") when it does not.
func remoteSSHControlFits(control string) bool {
	return len(control)-len("%C")+sshControlHashLen+sshControlTempSuffixLen <= unixSocketPathMax
}

// remoteSSHControlDir makes or accepts dir only as a real directory owned
// by this user with no group or other access, so another local user cannot
// plant or read the job's control socket.
func remoteSSHControlDir(dir string) (string, error) {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("ssh control directory %s is not a private directory of this user", dir)
	}
	return dir, nil
}

// options returns the reuse arguments, or nil when reuse is off.
func (s remoteSSH) options() []string {
	if s.control == "" {
		return nil
	}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + s.control, "-o", "ControlPersist=" + remoteSSHPersist}
}

// probe returns the fleet probe for this job: its connection becomes the
// job's master. Without reuse it is nil, the fleet's default probe.
func (s remoteSSH) probe() runnerfleet.ProbeFunc {
	if s.control == "" {
		return nil
	}
	return runnerfleet.ProbeVia(s.options()...)
}

// command runs remote on target over the job's connection.
func (s remoteSSH) command(ctx context.Context, connectTimeout, target, remote string) *exec.Cmd {
	args := append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=" + connectTimeout}, s.options()...)
	return exec.CommandContext(ctx, "ssh", append(args, target, remote)...)
}

// close ends the job's master for target. A master that a fleet probe
// opened to a host the job did not select ends after remoteSSHPersist.
func (s remoteSSH) close(target string) {
	if s.control == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "ssh", "-o", "ControlPath="+s.control, "-O", "exit", target).Run()
}
