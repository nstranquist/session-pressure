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
	if !MigrateLegacyStorageThresholds(&p) || p.Storage.WarningFreeBytes != 50<<30 || p.Storage.CriticalReleaseBytes != 15<<30 || !p.Storage.EnforceAdmission || p.AutoShedCritical {
		t.Fatalf("migration lost contract: %+v", p.Storage)
	}
	if MigrateLegacyStorageThresholds(&p) {
		t.Fatal("migration is not idempotent")
	}
}

func TestExplicitCriticalRecoveryMigrationPreservesOperatorSettings(t *testing.T) {
	for _, target := range []int64{50 << 30, 60 << 30} {
		p := DefaultPolicy(16 * 1024)
		p.Storage.CriticalReleaseBytes = 20 << 30
		p.Storage.TargetFreeBytes = target
		p.Storage.EnforceAdmission = true
		p.Storage.ProviderMinAgeSeconds = 9999
		p.Storage.BlockNewAt = LevelCritical
		before := p
		if !MigrateLegacyStorageThresholds(&p) {
			t.Fatal("former shipped threshold set not migrated")
		}
		before.Storage.CriticalReleaseBytes = 15 << 30
		if p.Storage != before.Storage || p.AutoShedCritical != before.AutoShedCritical {
			t.Fatalf("operator settings changed: %+v", p.Storage)
		}
		if MigrateLegacyStorageThresholds(&p) {
			t.Fatal("migration not idempotent")
		}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*StoragePolicy){
		func(s *StoragePolicy) { s.CriticalFreeBytes = 9 << 30 },
		func(s *StoragePolicy) { s.CriticalReleaseBytes = 18 << 30 },
		func(s *StoragePolicy) { s.RedFreeBytes = 24 << 30 },
		func(s *StoragePolicy) { s.RedReleaseBytes = 31 << 30 },
		func(s *StoragePolicy) { s.WarningFreeBytes = 49 << 30 },
		func(s *StoragePolicy) { s.WarningReleaseBytes = 61 << 30 },
		func(s *StoragePolicy) { s.TargetFreeBytes = 65 << 30 },
	} {
		p := DefaultPolicy(16 * 1024)
		p.Storage.CriticalReleaseBytes = 20 << 30
		mutate(&p.Storage)
		before := p.Storage
		if MigrateLegacyStorageThresholds(&p) || p.Storage != before {
			t.Fatalf("custom values changed: %+v", p.Storage)
		}
	}
}

func TestCriticalRecoveryMigrationPreservesDisabledStorage(t *testing.T) {
	p := DefaultPolicy(16 * 1024)
	p.Storage.CriticalReleaseBytes = 20 << 30
	p.Storage.Enabled = false
	p.Storage.EnforceAdmission = false
	if !MigrateLegacyStorageThresholds(&p) || p.Storage.Enabled || p.Storage.EnforceAdmission || p.Storage.CriticalReleaseBytes != 15<<30 {
		t.Fatalf("migration changed disabled authority: %+v", p.Storage)
	}
}
