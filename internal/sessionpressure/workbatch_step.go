package sessionpressure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// UnmarshalJSON keeps each batch step strict even though custom decoding would
// otherwise bypass the parent decoder's DisallowUnknownFields setting. It also
// rejects inline shell strings at the wire boundary: a batch is an argv
// contract, not a second shell language hidden inside JSON.
func (step *WorkBatchStep) UnmarshalJSON(body []byte) error {
	type rawWorkBatchStep WorkBatchStep
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var value rawWorkBatchStep
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode work batch step: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if inlineShellBatchArgv(value.Argv) {
		return fmt.Errorf("work batch step %q cannot use an inline shell command; declare the executable and argv directly", value.ID)
	}
	*step = WorkBatchStep(value)
	return nil
}

func inlineShellBatchArgv(command []string) bool {
	if len(command) < 2 {
		return false
	}
	name := strings.ToLower(filepath.Base(command[0]))
	args := command[1:]
	if name == "env" {
		for len(args) > 0 && (strings.Contains(args[0], "=") || strings.HasPrefix(args[0], "-")) {
			args = args[1:]
		}
		if len(args) < 2 {
			return false
		}
		name, args = strings.ToLower(filepath.Base(args[0])), args[1:]
	}
	switch name {
	case "sh", "bash", "zsh", "dash", "fish", "ksh":
		for _, argument := range args {
			if argument == "-c" || argument == "--command" || (strings.HasPrefix(argument, "-") && strings.Contains(strings.TrimPrefix(argument, "-"), "c")) {
				return true
			}
		}
	}
	return false
}
