package sessionpressurecmd

// StorageProviderContract is the static classification of one storage
// provider. The runtime inventory must emit the same ID and classification.
// Agent skills and docs compare their provider tables with this list, so a
// change to a provider class is a contract change.
type StorageProviderContract struct {
	ID             string `json:"id"`
	Classification string `json:"classification"`
	// Conditional providers are omitted when their tool is absent.
	Conditional bool `json:"conditional,omitempty"`
	// FullWipeRequired providers refuse --apply without --full-wipe.
	FullWipeRequired bool `json:"full_wipe_required,omitempty"`
}

// StorageProviderContracts returns the closed provider list in inventory
// order. Inventory order is not apply priority: go-build-cache appears early
// but is the last operator choice.
func StorageProviderContracts() []StorageProviderContract {
	return []StorageProviderContract{
		{ID: "browser-dead-profiles", Classification: string(storageProviderAutoSafe)},
		{ID: "pnpm-store", Classification: string(storageProviderAutoSafe)},
		{ID: "user-trash", Classification: string(storageProviderOperator)},
		{ID: "app-updater-caches", Classification: string(storageProviderOperator)},
		{ID: "browser-media-caches", Classification: string(storageProviderOperator)},
		{ID: "docker-reclaim", Classification: string(storageProviderOperator), Conditional: true},
		{ID: "ios-simulator-runtime", Classification: string(storageProviderReportOnly)},
		{ID: "go-build-cache", Classification: string(storageProviderOperator), FullWipeRequired: true},
		{ID: "yarn-cache", Classification: string(storageProviderOperator)},
		{ID: "npm-cache", Classification: string(storageProviderOperator)},
		{ID: "brew-cache", Classification: string(storageProviderOperator)},
		{ID: "swiftpm-cache", Classification: string(storageProviderOperator)},
		{ID: "playwright-cache", Classification: string(storageProviderOperator)},
		{ID: "brew-autoremove", Classification: string(storageProviderOperator)},
		{ID: "uv-cache", Classification: string(storageProviderOperator)},
		{ID: "gradle-caches", Classification: string(storageProviderOperator)},
		{ID: "xcode-derived-data", Classification: string(storageProviderOperator)},
		{ID: "nicos-telemetry-archive", Classification: string(storageProviderReportOnly)},
		{ID: "nicos-voice-models", Classification: string(storageProviderReportOnly)},
		{ID: "nicos-wine-proof", Classification: string(storageProviderReportOnly)},
		{ID: "nicos-pw-archive", Classification: string(storageProviderReportOnly)},
		{ID: "xcode-developer-data", Classification: string(storageProviderReportOnly)},
		{ID: "xcode-device-support", Classification: string(storageProviderReportOnly)},
		{ID: "go-module-cache", Classification: string(storageProviderReportOnly)},
		{ID: "cargo-registry", Classification: string(storageProviderReportOnly)},
		{ID: "library-caches", Classification: string(storageProviderReportOnly)},
		{ID: "downloads", Classification: string(storageProviderReportOnly)},
		{ID: "mobile-sync", Classification: string(storageProviderReportOnly)},
	}
}
