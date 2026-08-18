package operationcontract

import "testing"

func TestValidateOutputJSONCanFail(t *testing.T) {
	if err := ValidateOutputJSON(All(), pressureResultSchemaID, []byte(`{"ok":true,"action":"doctor"}`)); err != nil {
		t.Fatalf("valid envelope: %v", err)
	}
	if err := ValidateOutputJSON(All(), "not.a.schema", []byte(`{"ok":true,"action":"doctor"}`)); err == nil {
		t.Fatal("unknown schema must fail")
	}
	if err := ValidateOutputJSON(All(), pressureResultSchemaID, []byte(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
	if err := ValidateOutputJSON(All(), pressureResultSchemaID, []byte(`{"ok":true,"action":"doctor"}{"ok":true}`)); err == nil {
		t.Fatal("trailing JSON must fail")
	}
	if err := ValidateOutputJSON(All(), pressureResultSchemaID, []byte(`{"action":"doctor"}`)); err == nil {
		t.Fatal("missing ok must fail")
	}
	if err := ValidateOutputJSON(All(), pressureResultSchemaID, []byte(`{"ok":"yes","action":"doctor"}`)); err == nil {
		t.Fatal("non-bool ok must fail")
	}
}
