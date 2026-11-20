package vaulttemplate

import "embed"

// FS embeds the starter vault directory tree (including dotfiles like .gitkeep).
//
//go:embed all:CLAUDE.md all:company all:teams all:agents all:memory all:templates all:skills all:wiki all:sources all:inbox all:routines all:journal
var FS embed.FS
