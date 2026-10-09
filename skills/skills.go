// Package skills holds the agent skill files shipped with Marasi.
package skills

import _ "embed"

// Marasi is the agent skill file for the marasi CLI.
//
//go:embed marasi/SKILL.md
var Marasi string
