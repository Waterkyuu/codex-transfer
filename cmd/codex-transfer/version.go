package main

// releaseVersion is linker-injected build metadata and is never changed by application code.
// The variable exists for Go's -X linker option, rather than as mutable service state.
var releaseVersion = "development"
