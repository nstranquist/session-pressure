package sessionpressurecleanup

import "testing"

func TestInstallAndReset(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if Current().ListBrowser != nil {
		t.Fatal("expected empty providers after Reset")
	}
	Install(Providers{ListBrowser: func() ([]BrowserSession, error) { return nil, nil }})
	if Current().ListBrowser == nil {
		t.Fatal("Install did not stick")
	}
	Reset()
	if Current().ListBrowser != nil {
		t.Fatal("Reset left providers installed")
	}
}
