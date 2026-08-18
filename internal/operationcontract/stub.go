package operationcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const pressureResultSchemaID = "nicos.session.pressure-result.v1"

// All is the extract stand-in for the generated contract set. Catalog
// generation stays in nicos-tools; this package only validates the public
// JSON envelope tests already emit.
func All() any { return pressureResultSchemaID }

// ValidateOutputJSON fails closed on unknown schemas, invalid JSON, trailing
// values, and a missing typed ok/action envelope.
func ValidateOutputJSON(_ any, id string, body []byte) error {
	if id != pressureResultSchemaID {
		return fmt.Errorf("unknown output schema %q", id)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode output %s: %w", id, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode output %s: trailing JSON values", id)
		}
		return fmt.Errorf("decode output %s trailing data: %w", id, err)
	}
	okValue, okPresent := value["ok"]
	if !okPresent {
		return fmt.Errorf("$: missing required property %q", "ok")
	}
	if _, isBool := okValue.(bool); !isBool {
		return fmt.Errorf("$.ok: expected boolean, got %T", okValue)
	}
	actionValue, actionPresent := value["action"]
	if !actionPresent {
		return fmt.Errorf("$: missing required property %q", "action")
	}
	if _, isString := actionValue.(string); !isString {
		return fmt.Errorf("$.action: expected string, got %T", actionValue)
	}
	return nil
}
