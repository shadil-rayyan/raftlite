// Package ui embeds the single-page dashboard served by the measurement
// server. Kept dependency-free (vanilla JS + canvas charts) so the whole
// evaluation runs offline.
package ui

import _ "embed"

//go:embed index.html
var IndexHTML []byte
