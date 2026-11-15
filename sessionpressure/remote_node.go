package sessionpressure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nstranquist/session-pressure/runnerfleet"
)

// Node verification on the runner (work queue v2, slice 3).
//
// Only checks that return a verdict and no artifact go remote: test runners,
// `tsc --noEmit`, eslint and package test/typecheck/lint scripts. The job
// follows the runner contract in nicos-ops docs/gate-runner.md ("Frontend
// (Node) gates"): the Node version comes from the repository, the job gets a
// private HOME and npm cache, and a version the runner does not pin fails
// closed. Here an unpinned version is detected before upload, so the command
// declines and keeps local admission instead of failing.
//
// The pins mirror the runner installers: nicos-ops tools/runner-tools/node.go
// (nodePins) and tools/runner-tools/pnpm (pnpmPins).
var (
	runnerNodePins = []string{"26.13.0", "24.22.2"}
	runnerPNPMPins = []string{"11.22.0", "11.15.1", "9.15.0", "9.12.0"}
)

const (
	remoteNodeProfile   = "linux-node"
	remoteNodePNPMStore = "/srv/build/cache/pnpm-store"
)

// remoteNodePlan is everything the runner needs; it never holds file content.
type remoteNodePlan struct {
	root        string   // Git top level; the uploaded source root
	workdir     string   // command directory, relative to root
	installDir  string   // lockfile directory, relative to root
	manager     string   // npm or pnpm
	nodeVersion string   // exact runner pin
	pnpmVersion string   // exact runner pin when manager is pnpm
	argv        []string // the single verification command
	// placementKey joins this run to its local history (work_placer.go).
	placementKey string
	familyKey    string
}

// localToolVersion reports the Mac's version of a tool ("node" or "pnpm").
// Tests replace it.
var localToolVersion = func(tool string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tool, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}

// planRemoteNodeWork returns a plan, or the reason the command stays local.
func planRemoteNodeWork(cwd string, command []string) (remoteNodePlan, string) {
	argv, dir, reason := remoteNodeCommand(cwd, command)
	if reason != "" {
		return remoteNodePlan{}, reason
	}
	root, err := gitTopLevel(dir)
	if err != nil {
		return remoteNodePlan{}, "not inside a Git repository"
	}
	plan := remoteNodePlan{root: root, argv: argv}
	if plan.workdir, err = filepath.Rel(root, dir); err != nil {
		return remoteNodePlan{}, "command directory is outside the repository"
	}
	lockDir, lock := findUpward(dir, root, "pnpm-lock.yaml", "package-lock.json")
	switch lock {
	case "pnpm-lock.yaml":
		plan.manager = "pnpm"
	case "package-lock.json":
		plan.manager = "npm"
	default:
		return remoteNodePlan{}, "no lockfile (pnpm-lock.yaml or package-lock.json)"
	}
	plan.installDir, _ = filepath.Rel(root, lockDir)
	if base := strings.ToLower(filepath.Base(lightCommandTokens(argv)[0])); (base == "pnpm" && plan.manager != "pnpm") || (base == "npm" && plan.manager != "npm") {
		return remoteNodePlan{}, "command package manager does not match the lockfile"
	}
	declared := ""
	if versionDir, name := findUpward(dir, root, ".node-version", ".nvmrc"); name != "" {
		body, _ := os.ReadFile(filepath.Join(versionDir, name))
		declared = strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
	}
	if declared == "" {
		// Nothing declared: the Mac's own Node selects the runner pin of the
		// same major. 26.13.1 on the Mac runs on 26.13.0, never on 24.
		declared = strings.SplitN(strings.TrimPrefix(localToolVersion("node"), "v"), ".", 2)[0]
	}
	if plan.nodeVersion = matchPin(declared, runnerNodePins); plan.nodeVersion == "" {
		return remoteNodePlan{}, fmt.Sprintf("Node %q is not pinned on the runner", declared)
	}
	if plan.manager == "pnpm" {
		wanted := packageManagerVersion(filepath.Join(lockDir, "package.json"), "pnpm")
		if wanted == "" {
			wanted = localToolVersion("pnpm")
		}
		if plan.pnpmVersion = matchExact(wanted, runnerPNPMPins); plan.pnpmVersion == "" {
			return remoteNodePlan{}, fmt.Sprintf("pnpm %q is not pinned on the runner", wanted)
		}
	}
	if tool := macOnlyScriptTool(dir, argv); tool != "" {
		return remoteNodePlan{}, "package script uses Mac-only " + tool
	}
	return plan, ""
}

// macOnlyTools are commands a Linux runner cannot provide. A package script
// that names one would fail remotely with no fallback, so it stays local.
var macOnlyTools = []string{"xcrun", "xcodebuild", "swift", "swiftc", "osascript", "launchctl", "codesign", "security", "sw_vers", "defaults", "pbcopy", "pbpaste", "plutil", "ditto", "hdiutil", "open"}

// macOnlyScriptTool reads the npm/pnpm script the command runs from the
// nearest package.json and reports a Mac-only tool it names. It checks the
// named script and the scripts it runs through `npm run`/`pnpm run` one level
// deep; deeper indirection is not followed.
func macOnlyScriptTool(dir string, argv []string) string {
	tokens := lightCommandTokens(argv)
	base := strings.ToLower(filepath.Base(tokens[0]))
	if base != "npm" && base != "pnpm" {
		return ""
	}
	script, ok := nodePackageScript(base, tokens[1:])
	if !ok {
		return ""
	}
	pkgDir, name := findUpward(dir, "/", "package.json")
	if name == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(pkgDir, name))
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return ""
	}
	texts := []string{pkg.Scripts[script], pkg.Scripts["pre"+script], pkg.Scripts["post"+script]}
	for _, field := range strings.Fields(pkg.Scripts[script]) {
		if nested, ok := pkg.Scripts[field]; ok {
			texts = append(texts, nested)
		}
	}
	for _, text := range texts {
		for _, word := range strings.FieldsFunc(text, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '&' || r == '|' || r == ';' || r == '(' || r == ')' || r == '"' || r == '\''
		}) {
			word = filepath.Base(word)
			for _, tool := range macOnlyTools {
				if word == tool {
					return tool
				}
			}
		}
	}
	return ""
}

// remoteNodeCommand unwraps a single-statement shell command (cd allowed)
// and accepts only verification shapes that write nothing back.
func remoteNodeCommand(cwd string, command []string) ([]string, string, string) {
	argv, dir := command, cwd
	if statement, ok := shellCommandStatement(command); ok {
		commands, parsed := tokenizeSafeShellSequence(statement)
		if !parsed {
			return nil, "", "shell command is not a safe sequence"
		}
		argv = nil
		for _, statement := range commands {
			switch {
			case safeShellSetCommand(statement):
				continue
			case len(statement) == 2 && statement[0] == "cd":
				next, ok := safeShellWorkDir(dir, statement[1])
				if !ok || argv != nil {
					return nil, "", "shell cd is not a plain directory change before the command"
				}
				dir = next
			default:
				if argv != nil {
					// Compound statements (an edit then a test) must run here:
					// the edit has to change this checkout.
					return nil, "", "compound shell command"
				}
				argv = statement
			}
		}
		if argv == nil {
			return nil, "", "no command"
		}
	}
	if !remoteNodeVerification(argv) {
		return nil, "", "not a Node verification command"
	}
	return argv, dir, ""
}

var remoteNodeWriteBackFlags = map[string]bool{
	"-u": true, "--update": true, "--updateSnapshot": true, "--update-snapshots": true,
	"--fix": true, "--write": true, "--watch": true, "watch": true, "--coverage": true, "--ui": true,
}

func remoteNodeVerification(argv []string) bool {
	tokens := lightCommandTokens(argv)
	if len(tokens) < 1 {
		return false
	}
	for _, token := range tokens[1:] {
		flag, _, _ := strings.Cut(token, "=")
		if remoteNodeWriteBackFlags[flag] {
			return false
		}
	}
	base := strings.ToLower(filepath.Base(tokens[0]))
	rest := tokens[1:]
	switch base {
	case "npm", "pnpm":
		if class, ok := classifyNodeWorkArgv(tokens); ok && (class == WorkClassTest || class == WorkClassExpressTest) {
			return true
		}
		if base == "pnpm" && len(rest) >= 2 && (rest[0] == "exec" || rest[0] == "dlx") {
			return nodeCheckTool(rest[1], rest[2:])
		}
	case "npx":
		return len(rest) >= 1 && nodeCheckTool(rest[0], rest[1:])
	default:
		return nodeCheckTool(base, rest)
	}
	return false
}

func nodeCheckTool(tool string, args []string) bool {
	switch strings.ToLower(filepath.Base(tool)) {
	case "vitest", "jest", "eslint":
		return true
	case "tsc":
		for _, arg := range args {
			if arg == "--noEmit" || arg == "--noEmit=true" {
				return true
			}
		}
	}
	return false
}

// matchPin resolves "24", "v24", "24.x", "lts"-free exact or major forms to a
// runner pin. Only an exact pin or a bare major is accepted; ranges are not.
func matchPin(declared string, pins []string) string {
	declared = strings.TrimPrefix(strings.TrimSpace(declared), "v")
	declared = strings.TrimSuffix(declared, ".x")
	if declared == "" {
		return ""
	}
	if exact := matchExact(declared, pins); exact != "" {
		return exact
	}
	if strings.Count(declared, ".") == 0 {
		for _, pin := range pins {
			if strings.HasPrefix(pin, declared+".") {
				return pin
			}
		}
	}
	return ""
}

func matchExact(version string, pins []string) string {
	for _, pin := range pins {
		if pin == version {
			return pin
		}
	}
	return ""
}

// packageManagerVersion reads "pnpm@X[+hash]" from package.json.
func packageManagerVersion(path, manager string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return ""
	}
	name, version, ok := strings.Cut(pkg.PackageManager, "@")
	if !ok || name != manager {
		return ""
	}
	version, _, _ = strings.Cut(version, "+")
	return version
}

// findUpward returns the first directory from dir up to root that holds one
// of names (checked in order per directory) and the name found.
func findUpward(dir, root string, names ...string) (string, string) {
	for {
		for _, name := range names {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
				return dir, name
			}
		}
		if dir == root {
			return "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func gitTopLevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// remoteNodeRunnerCommand is the job shell. It runs from the uploaded source
// root. Missing pins exit 2 with the refusal marker; nothing falls back.
func remoteNodeRunnerCommand(plan remoteNodePlan, job string) string {
	nodeBin := "/srv/build/opt/node-v" + plan.nodeVersion + "-linux-x64/bin"
	path := nodeBin
	install := "npm ci --no-audit --no-fund --prefer-offline"
	if plan.manager == "pnpm" {
		pnpmBin := "/srv/build/opt/pnpm-" + plan.pnpmVersion + "/bin"
		path += ":" + pnpmBin
		install = "pnpm install --frozen-lockfile --prefer-offline --store-dir " + remoteNodePNPMStore
	}
	refuse := func(what string) string {
		return "{ printf '%s\\n' " + shellQuote(WorkRefusalMarker+" kind="+WorkRefusalRemoteProfile+": "+what+" is not installed on the runner; local fallback is disabled") +
			fmt.Sprintf(" >&2; exit %d; }", workExitPolicyDenied)
	}
	check := "test -x " + shellQuote(nodeBin+"/node") + " || " + refuse("Node "+plan.nodeVersion)
	if plan.manager == "pnpm" {
		check += "; test -x " + shellQuote("/srv/build/opt/pnpm-"+plan.pnpmVersion+"/bin/pnpm") + " || " + refuse("pnpm "+plan.pnpmVersion)
	}
	quoted := make([]string, len(plan.argv))
	for i, arg := range plan.argv {
		quoted[i] = shellQuote(arg)
	}
	private := "/srv/build/candidates/pressure-" + job + "/private"
	return check + "; R=$(pwd) && " +
		"mkdir -p " + shellQuote(private+"/home") + " " + shellQuote(private+"/npm-cache") + " && " +
		"export HOME=" + shellQuote(private+"/home") + " npm_config_cache=" + shellQuote(private+"/npm-cache") +
		" CI=1 PATH=" + shellQuote(path) + ":\"$R/" + filepath.ToSlash(plan.installDir) + "/node_modules/.bin\":/srv/build/bin:/usr/bin:/bin && " +
		"cd \"$R/" + filepath.ToSlash(plan.installDir) + "\" && " + install + " && " +
		"cd \"$R/" + filepath.ToSlash(plan.workdir) + "\" && exec env " + strings.Join(quoted, " ")
}

// runRemoteNodeWork uploads the repository inventory, runs the plan on the
// fleet-bound runner and streams its result. It mirrors TryRemoteWork's Go
// path without artifacts.
func runRemoteNodeWork(dir string, policy RemoteWorkPolicy, plan remoteNodePlan, options WorkRunOptions, streams WorkRunStreams) (exitCode int, resultErr error) {
	job, err := NewWorkOperationID()
	if err != nil {
		return 1, err
	}
	modules, common, scope := []string{plan.root}, plan.root, remoteScopeRepository
	sourceBytes, err := remoteSourceSize(modules, common, scope, remoteSourceLimit)
	if err != nil {
		return rejectRemoteNode(dir, err.Error())
	}
	if policy.Fleet != "" {
		binding, bindErr := runnerfleet.Assign(context.Background(), policy.Fleet, runnerfleet.Request{
			Key: "pressure-" + job, Identity: plan.root + "\x00" + strings.Join(plan.argv, "\x00"),
			Repository: "session-pressure-work", Profile: "linux-native", Lane: "heavy",
		}, nil)
		if bindErr != nil {
			return rejectRemoteNode(dir, "fleet assignment: "+bindErr.Error())
		}
		policy.Host, policy.User = binding.Host.Name, binding.Host.User
	}
	started := time.Now()
	timingMS := map[string]int64{}
	stageStarted, stage := started, "preparation_ms"
	finishStage := func() { timingMS[stage] += time.Since(stageStarted).Milliseconds() }
	nextStage := func(name string) { finishStage(); stage, stageStarted = name, time.Now() }
	uploadMode, uploadFiles, uploadBytes := "", 0, int64(0)
	defer func() {
		finishStage()
		event := map[string]any{
			"event": "remote_work_finished", "duration_ms": time.Since(started).Milliseconds(), "timing_ms": timingMS,
			"source_bytes": sourceBytes, "operation": "node", "tool_family": ToolFamilyNode, "profile": remoteNodeProfile,
			"package_manager": plan.manager, "node_version": plan.nodeVersion, "runner_host": policy.Host, "runner_user": policy.User,
			"request_key": "pressure-" + job, "exit_code": exitCode, "failed": resultErr != nil || exitCode != 0,
			"source_upload_mode": uploadMode, "source_files_sent": uploadFiles, "source_bytes_sent": uploadBytes,
			"placement_key": plan.placementKey, "placement_family_key": plan.familyKey,
		}
		for name, ms := range timingMS {
			event[name] = ms
		}
		recordControlEvent(dir, event)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	ssh := func(command string) *exec.Cmd {
		return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", policy.User+"@"+policy.Host, command)
	}
	remote := "/srv/build/candidates/pressure-" + job
	requestDir := filepath.Join(dir, "remote-requests")
	if err := os.MkdirAll(requestDir, 0o700); err != nil {
		return 1, err
	}
	request, _ := json.Marshal(map[string]any{"host": policy.Host, "user": policy.User, "request_key": "pressure-" + job, "profile": remoteNodeProfile, "remote_path": remote, "started_at": started.UTC()})
	if err := atomicWrite(filepath.Join(requestDir, job+".json"), append(request, '\n'), 0o600); err != nil {
		return 1, err
	}
	nextStage("source_upload_ms")
	uploaded := false
	if remoteSourceDeltaEnabled() {
		up, deltaErr := uploadRemoteSourcesDelta(ssh, streams.Stderr, dir, policy.Host, remote, "pressure-"+job, common, modules, scope)
		switch {
		case errors.Is(deltaErr, errRemoteSourceUnsupported):
		case deltaErr != nil:
			return 1, fmt.Errorf("remote source upload failed (%s): %w", up.mode, deltaErr)
		default:
			uploaded, uploadMode, uploadFiles, uploadBytes = true, up.mode, up.files, up.bytes
		}
	}
	if !uploaded {
		upload := ssh("timeout 600 flock -w 600 /srv/build/input-transfer.lock sh -c " + shellQuote("umask 077; mkdir -p "+shellQuote(remote+"/source")+" && printf '%s\n' "+shellQuote("pressure-"+job)+" > "+shellQuote(remote+".request")+" && tar --same-permissions -xzf - -C "+shellQuote(remote+"/source")))
		pipe, err := upload.StdinPipe()
		if err != nil {
			return 1, err
		}
		upload.Stderr = streams.Stderr
		if err = upload.Start(); err != nil {
			return 1, err
		}
		writeErr := writeRemoteSources(pipe, modules, common, scope)
		_ = pipe.Close()
		if err = upload.Wait(); writeErr != nil || err != nil {
			return 1, fmt.Errorf("remote source upload failed: %w", errors.Join(writeErr, err))
		}
		uploadMode, uploadBytes = remoteUploadFull, sourceBytes
	}
	cleanupSafe := false
	defer func() {
		nextStage("cleanup_ms")
		if !cleanupSafe {
			fmt.Fprintf(streams.Stderr, "session-pressure: retained remote inputs at %s; request key pressure-%s (completion not confirmed)\n", remote, job)
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", policy.User+"@"+policy.Host, "rm -rf -- "+shellQuote(remote)+" "+shellQuote(remote+".request")).Run()
	}()
	command := remoteNodeRunnerCommand(plan, job)
	command = "env NCD_RUNNER_FRAMED=1 NCD_RUNNER_CLASS=heavy" + remoteDeliveryLineage(os.Getenv) + " NCD_RUNNER_KEY=" + shellQuote("pressure-"+job) +
		" /srv/build/run-job.sh session-pressure " + shellQuote("session-pressure:"+job) + " --candidate-dir " + shellQuote(remote+"/source") +
		" --timeout 1800 --shell " + shellQuote(command)
	command = "if test -x /srv/build/gaterunner && /srv/build/gaterunner inspect --key " + shellQuote("pressure-"+job) + " >/dev/null 2>&1; then exec env NCD_RUNNER_FRAMED=1 /srv/build/gaterunner wait --key " + shellQuote("pressure-"+job) + "; else exec " + command + "; fi"
	install := "npm ci"
	if plan.manager == "pnpm" {
		install = "pnpm " + plan.pnpmVersion + " install"
	}
	fmt.Fprintf(streams.Stderr, "session-pressure: routing Node check to %s (Node %s, %s, durable runner queue, heavy lane)\n", policy.Host, plan.nodeVersion, install)
	nextStage("runner_wait_and_run_ms")
	cleanupSafe, err = runRunnerSubscriber(ctx, func() *exec.Cmd { return ssh(command) }, streams.Stdout, streams.Stderr)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// rejectRemoteNode refuses after a plan was accepted (source or fleet
// problems). Local fallback is disabled from this point, as for Go.
func rejectRemoteNode(dir, reason string) (int, error) {
	_, code, err := rejectRemoteWork(dir, "node", reason)
	return code, err
}

// declineRemoteNode records why eligible-looking Node work stays local.
func declineRemoteNode(dir, reason string) {
	recordControlEvent(dir, map[string]any{"event": "remote_work_declined", "tool_family": ToolFamilyNode, "profile": remoteNodeProfile, "reason": boundedText(reason, 120)})
}

// tryRemoteNodeWork is the non-Go branch of TryRemoteWork. A command that is
// not Node verification is not handled. An eligible-looking command whose
// toolchain or layout the runner cannot serve declines before upload and
// keeps local admission, with the reason recorded.
func tryRemoteNodeWork(dir string, policy RemoteWorkPolicy, options WorkRunOptions, streams WorkRunStreams) (bool, int, error) {
	if !policy.Node || options.Placement == WorkPlacementLocal || WorkToolFamily(options.Command) != ToolFamilyNode {
		return false, 0, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false, 0, nil
	}
	plan, reason := planRemoteNodeWork(cwd, options.Command)
	if reason != "" {
		declineRemoteNode(dir, reason)
		return false, 0, nil
	}
	if streams.Stdout == nil {
		streams.Stdout = os.Stdout
	}
	if streams.Stderr == nil {
		streams.Stderr = os.Stderr
	}
	keys := NewPlacementKeys(cwd, options.Command)
	plan.placementKey, plan.familyKey = keys.Exact, keys.Family
	if placeLocally(dir, policy, options, keys, ToolFamilyNode, streams.Stderr) {
		return false, 0, nil
	}
	code, err := runRemoteNodeWork(dir, policy, plan, options, streams)
	return true, code, err
}
