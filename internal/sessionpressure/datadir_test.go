package sessionpressure

import (
	"path/filepath"
	"testing"
)

func TestDataDirPrefersCanonicalEnv(t *testing.T) {
	canonical := t.TempDir()
	alias := t.TempDir()
	t.Setenv(DataDirEnv, canonical)
	t.Setenv(DataDirEnvAlias, alias)
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DataDir() = %s, want canonical %s", got, want)
	}
}

func TestDataDirDefaultIsNicosHome(t *testing.T) {
	t.Setenv(DataDirEnv, "")
	t.Setenv(DataDirEnvAlias, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".nicos-dev", "session-pressure")
	if got != want {
		t.Fatalf("DataDir() = %s, want %s", got, want)
	}
}

func TestDataDirHonorsPublicAlias(t *testing.T) {
	alias := t.TempDir()
	t.Setenv(DataDirEnv, "")
	t.Setenv(DataDirEnvAlias, alias)
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DataDir() = %s, want alias %s", got, want)
	}
}
