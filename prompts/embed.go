package prompts

import _ "embed"

// Kernel is the embedded Max operating kernel (prompts/kernel.md, <= 6000 chars).
//
//go:embed kernel.md
var Kernel string

// Learning is the embedded system prompt for read-only gated learning passes (prompts/learning.md).
//
//go:embed learning.md
var Learning string
