package handoff

import "os"

// userHome is os.UserHomeDir behind a variable so that a test can pin it and
// get the same shortened paths on every machine.
var userHome = os.UserHomeDir
