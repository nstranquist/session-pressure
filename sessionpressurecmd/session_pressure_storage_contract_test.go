package sessionpressurecmd

import (
	"slices"
	"testing"

	"github.com/nstranquist/session-pressure/sessionpressure"
)

func TestStorageProviderContractsMatchInventory(t *testing.T) {
	withStorageProviderFakes(t)
	storageDockerAvailable = func() (string, bool, error) { return "/usr/local/bin/docker", true, nil }
	storageDockerReclaimable = func() (int64, error) { return 0, nil }

	reports, err := inspectStorageProviders(sessionpressure.DefaultPolicy(16<<10), false)
	if err != nil {
		t.Fatal(err)
	}
	contracts := StorageProviderContracts()
	if len(reports) != len(contracts) {
		t.Fatalf("inventory has %d providers, contract has %d", len(reports), len(contracts))
	}
	for i, report := range reports {
		want := contracts[i]
		if report.ID != want.ID || string(report.Classification) != want.Classification {
			t.Fatalf("provider %d = %s/%s, contract = %s/%s", i, report.ID, report.Classification, want.ID, want.Classification)
		}
	}
}

func TestStorageProviderContractsAgreeWithApplyGates(t *testing.T) {
	var autoSafe []string
	seen := map[string]bool{}
	for _, contract := range StorageProviderContracts() {
		if seen[contract.ID] {
			t.Fatalf("duplicate provider %s", contract.ID)
		}
		seen[contract.ID] = true
		if contract.Classification == string(storageProviderAutoSafe) {
			autoSafe = append(autoSafe, contract.ID)
		}
		if contract.FullWipeRequired != (contract.ID == "go-build-cache") {
			t.Fatalf("full-wipe gate mismatch for %s", contract.ID)
		}
	}
	if !slices.Equal(autoSafe, storageAutoSafeApplyIDs) {
		t.Fatalf("auto_safe contract %v != apply set %v", autoSafe, storageAutoSafeApplyIDs)
	}
}
