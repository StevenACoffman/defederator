package execengine

import _ "embed"

// Source is the full text of execengine.go. The generator reads it, renames the
// package declaration, and writes it to the output package as federation_exec.go —
// giving generated code access to the executor without a defederator import.
//
//go:embed execengine.go
var Source string
