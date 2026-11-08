package sessionpressure

import "testing"

func TestExplicitLegacyStorageThresholdMigration(t *testing.T) {
	p := DefaultPolicy(16 * 1024)
	p.Storage.WarningFreeBytes = 20 << 30
	p.Storage.RedFreeBytes = 10 << 30
	p.Storage.CriticalFreeBytes = 5 << 30
	p.Storage.WarningReleaseBytes = 25 << 30
	p.Storage.RedReleaseBytes = 15 << 30
	p.Storage.CriticalReleaseBytes = 8 << 30
	p.Storage.TargetFreeBytes = 25 << 30
	p.Storage.EnforceAdmission = true
	custom := p
	custom.Storage.RedFreeBytes = 11 << 30
	if MigrateLegacyStorageThresholds(&custom) || custom.Storage.RedFreeBytes != 11<<30 {
		t.Fatal("operator thresholds changed")
	}
	if !MigrateLegacyStorageThresholds(&p) || p.Storage.WarningFreeBytes != 50<<30 || p.Storage.CriticalReleaseBytes != 20<<30 || !p.Storage.EnforceAdmission || p.AutoShedCritical {
		t.Fatalf("migration lost contract: %+v", p.Storage)
	}
	if MigrateLegacyStorageThresholds(&p) {
		t.Fatal("migration is not idempotent")
	}
}
