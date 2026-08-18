package sessionpressurecontrol

import (
	"reflect"
	"strings"
	"testing"
)

func TestStorageAutoSafeApplyBuildsTypedArgv(t *testing.T) {
	registry := newActionRegistry()
	args, err := registry.build("storage.auto_safe.apply", map[string]string{"target_free": "30GiB"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--json", "session", "pressure", "storage", "apply", "--auto-safe", "--apply", "--target-free", "30GiB"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv=%q want=%q", args, want)
	}
	t.Logf("typed auto_safe argv: %s", strings.Join(args, " "))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, ";") || strings.Contains(joined, "rm ") || strings.Contains(joined, "go-build-cache") {
		t.Fatalf("auto_safe argv leaked a shell or operator provider: %q", args)
	}
}

func TestStorageAutoSafeApplyRejectsProviderAndShellText(t *testing.T) {
	registry := newActionRegistry()
	if _, err := registry.build("storage.auto_safe.apply", map[string]string{"provider": "go-build-cache"}); err == nil {
		t.Fatal("auto_safe apply accepted a named provider")
	}
	if _, err := registry.build("storage.auto_safe.apply", map[string]string{"target_free": "30GiB; rm -rf /"}); err == nil {
		t.Fatal("auto_safe apply accepted shell text as target_free")
	}
	if _, err := registry.build("storage.provider.apply", map[string]string{"provider": "go-build-cache; rm -rf"}); err == nil {
		t.Fatal("named provider apply accepted shell text")
	}
}

func TestStorageProviderApplyStillBuildsNamedArgv(t *testing.T) {
	registry := newActionRegistry()
	args, err := registry.build("storage.provider.apply", map[string]string{"provider": "user-trash"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--json", "session", "pressure", "storage", "apply", "--provider", "user-trash", "--apply"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv=%q want=%q", args, want)
	}
}
