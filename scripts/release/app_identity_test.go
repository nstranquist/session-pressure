package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureApp(t *testing.T, version, build string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NDev Pressure.app")
	for _, dir := range []string{"Contents/MacOS", "Contents/Resources", "Contents/Library/LaunchServices"} {
		if err := os.MkdirAll(filepath.Join(path, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	plist := fmt.Sprintf(`<plist><dict><key>CFBundleIdentifier</key><string>com.nstranquist.ndev-pressure</string><key>CFBundleExecutable</key><string>NDevPressure</string><key>CFBundleShortVersionString</key><string>%s</string><key>CFBundleVersion</key><string>%s</string></dict></plist>`, version, build)
	for name, data := range map[string]string{"Contents/Info.plist": plist, "Contents/MacOS/NDevPressure": "executable", "Contents/Library/LaunchServices/NDevPressureTraceHelper": "helper", "Contents/Resources/icon": "icon"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestAppAuthorityBindsWholeBundle(t *testing.T) {
	p := fixtureApp(t, "1.3.0", "5")
	before, err := readAppIdentity(p)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Exists || before.Version != "1.3.0" || before.Build != "5" || len(before.Digest) != 64 {
		t.Fatalf("identity: %+v", before)
	}
	for _, name := range []string{"Contents/Resources/icon", "Contents/Library/LaunchServices/NDevPressureTraceHelper", "Contents/MacOS/NDevPressure"} {
		if err := os.WriteFile(filepath.Join(p, name), []byte("changed"), 0755); err != nil {
			t.Fatal(err)
		}
		after, err := readAppIdentity(p)
		if err != nil {
			t.Fatal(err)
		}
		if before.Digest == after.Digest {
			t.Fatalf("unbound %s", name)
		}
		before = after
	}
	if err := os.Symlink("/outside", filepath.Join(p, "Contents/Resources/alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAppIdentity(p); err == nil {
		t.Fatal("external symlink accepted")
	}
}

func TestAppPromotionRejectsStaleIntentAndDowngradesBeforeMutation(t *testing.T) {
	currentPath := fixtureApp(t, "1.3.0", "5")
	oldPath := fixtureApp(t, "1.1.0", "3")
	current, err := readAppIdentity(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, expected string
		rollback       bool
		want           string
	}{
		{"missing intent", "", false, "requires --expected-current"},
		{"stale intent", strings.Repeat("a", 64), false, "authority changed"},
		{"rollback cannot bypass stale intent", strings.Repeat("a", 64), true, "authority changed"},
		{"old signed version", current.Digest, false, "downgrade"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := installApp(oldPath, currentPath, tc.expected, tc.rollback)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wrong refusal: %v", err)
			}
			after, err := readAppIdentity(currentPath)
			if err != nil {
				t.Fatal(err)
			}
			if after != current {
				t.Fatal("refused promotion changed authority")
			}
		})
	}
}

func TestAppVersionAndBuildOrdering(t *testing.T) {
	current := appIdentity{Exists: true, Digest: strings.Repeat("a", 64), Version: "1.3.0", Build: "5.2"}
	for _, tc := range []struct {
		version, build      string
		rollback, wantError bool
	}{
		{"1.3.0", "5.2", false, false}, {"1.3.0", "5.3", false, false},
		{"1.3.0", "6", false, false}, {"1.4.0", "1", false, false},
		{"1.3.0", "5.1", false, true}, {"1.3.0", "5.1", true, false},
		{"1.2.9", "99", false, true}, {"1.2.9", "99", true, false},
		{"1.3.0-beta.1", "99", false, true},
	} {
		candidate := appIdentity{Exists: true, Digest: strings.Repeat("b", 64), Version: tc.version, Build: tc.build}
		err := validateAppPromotion(current, candidate, current.Digest, tc.rollback)
		if (err != nil) != tc.wantError {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	if err := validateAppPromotion(appIdentity{}, current, "none", false); err != nil {
		t.Fatal(err)
	}
	if err := validateAppPromotion(current, appIdentity{}, current.Digest, false); err == nil {
		t.Fatal("missing candidate accepted")
	}
}

func TestAppIdentityRejectsMalformedMetadata(t *testing.T) {
	for _, pair := range [][2]string{{"bad", "1"}, {"1.2.3", "-1"}, {"1.2.3", "1.2.3.4"}, {"1.2.3", "+2"}} {
		if _, err := readAppIdentity(fixtureApp(t, pair[0], pair[1])); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	p := fixtureApp(t, "1.3.0", "5")
	plist := filepath.Join(p, "Contents/Info.plist")
	raw, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "</dict>", "<key>CFBundleVersion</key><string>3</string></dict>", 1))
	if err := os.WriteFile(plist, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAppIdentity(p); err == nil {
		t.Fatal("duplicate identity accepted")
	}
}
