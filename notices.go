// Package notices provides the third-party notices shipped with each binary.
package notices

import (
	_ "embed"
	"io"
)

//go:embed THIRD_PARTY_NOTICES.md
var text string

// Write writes the original notice bytes without a heading or extra newline.
func Write(output io.Writer) error {
	_, err := io.WriteString(output, text)
	return err
}
