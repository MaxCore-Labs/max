package domain

// EngineName identifies a supported CLI engine.
type EngineName string

const (
	EngineAgy    EngineName = "agy"
	EngineClaude EngineName = "claude"
	EngineCodex  EngineName = "codex"
)
