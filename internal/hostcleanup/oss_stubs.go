package hostcleanup

import (
	"time"

	"github.com/nstranquist/session-pressure/internal/devsession"
	"github.com/nstranquist/session-pressure/third_party/pageskein/browser"
)

func stubExpireBrowser(string, int, time.Duration, bool, time.Time) (browser.IdleExpiryResult, error) {
	return browser.IdleExpiryResult{}, nil
}
func stubTeardownDev(string, string, devsession.IdleTeardownExpectation) (bool, string, error) {
	return false, "oss-extract", nil
}
