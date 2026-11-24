package sessionpressure

import (
	"path/filepath"
	"strings"
)

// diskWriteKernelNameBytes is the length of a cut-off process name. The
// kernel keeps p_comm in MAXCOMLEN (16) bytes, so a longer program name
// arrives as its first 16 bytes ("nicos-system-backup" as
// "nicos-system-bac").
const diskWriteKernelNameBytes = 16

// diskWriterFullName returns the program file's base name when comm is the
// kernel's cut-off prefix of it, and comm otherwise. Only a name of exactly
// diskWriteKernelNameBytes can be cut, so callers read the path for those
// names alone. A process that renamed itself keeps its own name: the path
// is used only when comm is a strict prefix of the path's base name.
func diskWriterFullName(comm, path string) string {
	if len(comm) != diskWriteKernelNameBytes || path == "" {
		return comm
	}
	base := filepath.Base(path)
	if len(base) <= len(comm) || !strings.HasPrefix(base, comm) {
		return comm
	}
	return base
}

// diskWriterNameIsCutOf reports whether short is the kernel's cut-off form
// of full, after both went through privacySafeExecutable. The process
// sampler names processes from p_comm, so its ownership name for a long
// program is the cut form and must not replace the disk scan's full name.
func diskWriterNameIsCutOf(short, full string) bool {
	return len(short) == diskWriteKernelNameBytes && len(full) > len(short) && strings.HasPrefix(full, short)
}
