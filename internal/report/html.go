package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"strings"
)

//go:embed report.html.tmpl
var htmlSource string

var htmlTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"lower": strings.ToLower,
	"cell": func(v *string) string {
		if v == nil {
			return "NULL"
		}
		return *v
	},
	"isNull":       func(v *string) bool { return v == nil },
	"plural":       plural,
	"add":          func(a, b int) int { return a + b },
	"firstOfBatch": func(ds []Delivery, i int) bool { return i == 0 || ds[i-1].Batch != ds[i].Batch },
}).Parse(htmlSource))

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// HTML writes rep as a single self-contained page: no scripts, no external
// styles or fonts, readable on a phone.
func HTML(w io.Writer, rep *Report) error {
	return htmlTemplate.Execute(w, rep)
}
