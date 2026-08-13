package sessionpressure

// ProcessStartIdentity exposes the existing kernel identity boundary for
// interactive trace handoff. Callers must still revalidate it immediately
// before observing the target process.
func ProcessStartIdentity(pid int) (string, error) {
	return processStartIdentity(pid)
}
