package sessionpressure

// MigrateLegacyStorageThresholds upgrades only the exact former shipped
// threshold set. Operator-specific thresholds and authority switches survive.
// This runs only for the explicit policy migrate command.
func MigrateLegacyStorageThresholds(policy *Policy) bool {
	s := &policy.Storage
	if s.WarningFreeBytes != 20<<30 || s.RedFreeBytes != 10<<30 || s.CriticalFreeBytes != 5<<30 || s.WarningReleaseBytes != 25<<30 || s.RedReleaseBytes != 15<<30 || s.CriticalReleaseBytes != 8<<30 || s.TargetFreeBytes != 25<<30 {
		return false
	}
	s.WarningFreeBytes = 50 << 30
	s.RedFreeBytes = 25 << 30
	s.CriticalFreeBytes = 10 << 30
	s.WarningReleaseBytes = 60 << 30
	s.RedReleaseBytes = 30 << 30
	s.CriticalReleaseBytes = 20 << 30
	s.TargetFreeBytes = 60 << 30
	return true
}
