package prompts

import _ "embed"

// Kernel is the embedded Max operating kernel (prompts/kernel.md, <= 6000 chars).
//
//go:embed kernel.md
var Kernel string
