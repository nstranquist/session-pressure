//go:build !darwin

package sessionpressure

// Other hosts retain the established bounded timer. Native notification can be
// added per platform without changing acquisition or durable state semantics.
func watchWorkCapacity(string) (<-chan struct{}, func()) { return nil, func() {} }
