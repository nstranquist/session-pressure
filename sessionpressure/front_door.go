package sessionpressure

import (
	"os"
	"strings"

	"github.com/nstranquist/session-pressure/frontdoor"
)

// RecordFrontDoorFromEnv stores the wrapper's front-door registration
// (frontdoor.Env) with the revision it links (FrontDoorRevisionEnv), so a
// later `make release-promote` can rebuild that front door even when it was
// not started through it. It is best effort: a bad or missing value changes
// nothing and never fails the command. The usual cost is one small read.
func RecordFrontDoorFromEnv(getenv func(string) string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	value := strings.TrimSpace(getenv(frontdoor.Env))
	if value == "" {
		return
	}
	registration, err := frontdoor.Parse(value)
	if err != nil {
		return
	}
	registration.Revision = strings.TrimSpace(getenv(FrontDoorRevisionEnv))
	dir, err := DataDir()
	if err != nil {
		return
	}
	_, _ = frontdoor.Record(dir, registration)
}
