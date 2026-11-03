package sessionpressure

import (
	"debug/buildinfo"
	"os"
	"runtime/debug"
	"strings"
)

// A host runs several binaries built from this module: the CLI, the resident
// helper, and often a front door that links the library in-process (for
// example agent-launch admission). They are promoted by different steps, so a
// partial promotion leaves them on different source revisions while each one
// looks healthy. Every binary therefore carries the revision it was built from.
//
// Release and committed builds stamp it at link time:
//
//	-ldflags "-X github.com/nstranquist/session-pressure/sessionpressure.sourceRevision=<git sha>[-dirty]"
//
// A standalone build of this module falls back to Go's own vcs stamp.
var sourceRevision string

const (
	sourceRevisionModule = "github.com/nstranquist/session-pressure"
	sourceRevisionSymbol = sourceRevisionModule + "/sessionpressure.sourceRevision"

	// FrontDoorRevisionEnv carries the revision linked into a wrapper that
	// execs this CLI, so artifact status can compare all three binaries.
	FrontDoorRevisionEnv = "SESSION_PRESSURE_FRONT_DOOR_REVISION"
	// PromoteCommandEnv lets a host wrapper name its full promotion command.
	// Without it, hints fall back to `session-pressure monitor install`.
	PromoteCommandEnv = "SESSION_PRESSURE_PROMOTE_COMMAND"
)

// SourceRevision returns the session-pressure source revision this binary was
// built from, or "" when the build carries no evidence.
func SourceRevision() string {
	if value := strings.TrimSpace(sourceRevision); value != "" {
		return value
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return revisionFromBuildInfo(info)
}

// BinarySourceRevision reads the stamped revision from another executable
// without running it.
func BinarySourceRevision(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	return revisionFromBuildInfo(info)
}

func revisionFromBuildInfo(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	var ldflags, revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "-ldflags":
			ldflags = setting.Value
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if stamped := stampedSourceRevision(ldflags); stamped != "" {
		return stamped
	}
	// Go's vcs stamp names the main module's repository, so it identifies
	// session-pressure source only when this module is the main module.
	if info.Main.Path != sourceRevisionModule || revision == "" {
		return ""
	}
	if modified {
		return revision + "-dirty"
	}
	return revision
}

// stampedSourceRevision finds `-X <symbol>=<value>` in a recorded -ldflags
// value. Both `-X a=b` and `-X=a=b` spellings are accepted.
func stampedSourceRevision(ldflags string) string {
	fields := strings.Fields(ldflags)
	for index, field := range fields {
		var assignment string
		switch {
		case field == "-X" || field == "--X":
			if index+1 < len(fields) {
				assignment = fields[index+1]
			}
		case strings.HasPrefix(field, "-X="):
			assignment = strings.TrimPrefix(field, "-X=")
		case strings.HasPrefix(field, "--X="):
			assignment = strings.TrimPrefix(field, "--X=")
		}
		assignment = strings.Trim(assignment, `"'`)
		if value, ok := strings.CutPrefix(assignment, sourceRevisionSymbol+"="); ok {
			return strings.Trim(value, `"'`)
		}
	}
	return ""
}

// ArtifactSourceRevisions compares the source revisions of the binaries that
// execute on this host. Only executing binaries decide Mismatch: the CLI that
// answered, the front door that execed it, and the installed resident helper.
// The unpromoted candidate is informational; ArtifactDrift already reports it.
type ArtifactSourceRevisions struct {
	CLI             string `json:"cli,omitempty"`
	FrontDoor       string `json:"front_door,omitempty"`
	InstalledHelper string `json:"installed_helper,omitempty"`
	CandidateHelper string `json:"candidate_helper,omitempty"`
	Checked         bool   `json:"checked"`
	Mismatch        bool   `json:"mismatch"`
	Detail          string `json:"detail,omitempty"`
}

func inspectSourceRevisions(cli, frontDoor, installedHelper, candidateHelper string) ArtifactSourceRevisions {
	revisions := ArtifactSourceRevisions{
		CLI:             strings.TrimSpace(cli),
		FrontDoor:       strings.TrimSpace(frontDoor),
		InstalledHelper: strings.TrimSpace(installedHelper),
		CandidateHelper: strings.TrimSpace(candidateHelper),
	}
	known := make([]string, 0, 3)
	for _, value := range []string{revisions.CLI, revisions.FrontDoor, revisions.InstalledHelper} {
		if value != "" {
			known = append(known, value)
		}
	}
	if len(known) < 2 {
		revisions.Detail = "fewer than two executing binaries carry a source revision"
		return revisions
	}
	revisions.Checked = true
	for _, value := range known[1:] {
		if value != known[0] {
			revisions.Mismatch = true
			break
		}
	}
	if revisions.Mismatch {
		revisions.Detail = "executing session-pressure binaries were built from different source revisions; rebuild and promote all of them from one revision"
	}
	return revisions
}

// AgreementSummary names exactly which executing binaries were compared, so
// an unknown front door is never reported as agreeing.
func (revisions ArtifactSourceRevisions) AgreementSummary() string {
	var known, unknown []string
	for _, entry := range []struct{ name, value string }{
		{"cli", revisions.CLI}, {"front door", revisions.FrontDoor}, {"helper", revisions.InstalledHelper},
	} {
		if entry.value == "" {
			unknown = append(unknown, entry.name)
		} else {
			known = append(known, entry.name)
		}
	}
	summary := strings.Join(known, ", ") + " agree"
	if len(unknown) > 0 {
		summary += "; " + strings.Join(unknown, ", ") + " unknown"
	}
	return summary
}

// PromoteCommand is the host's full promotion command for operator hints.
func PromoteCommand() string {
	if command := strings.TrimSpace(os.Getenv(PromoteCommandEnv)); command != "" {
		return command
	}
	return "session-pressure monitor install"
}

// ShortRevision abbreviates a revision for human output, keeping a -dirty mark.
func ShortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return "unknown"
	}
	base, dirty := strings.CutSuffix(revision, "-dirty")
	if len(base) > 12 {
		base = base[:12]
	}
	if dirty {
		return base + "-dirty"
	}
	return base
}
