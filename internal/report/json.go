package report

import (
	"encoding/json"
	"io"
)

// JSON writes rep as indented JSON.
func JSON(w io.Writer, rep *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(rep)
}
