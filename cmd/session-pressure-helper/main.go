// session-pressure-helper is the deliberately tiny resident half of
// SessionPressure. The product CLI owns policy and lifecycle UX;
// this helper imports only the sampler/monitor packages so idle RSS stays low.
package main

import "github.com/nstranquist/session-pressure/internal/helpermain"

func main() {
	helpermain.Main()
}
