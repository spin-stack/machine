// SPDX-License-Identifier: Apache-2.0

package docsrec

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// page is a rendered page's frame: a page that fetches nothing, whose tables stay readable when a
// cell runs to a paragraph.
var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - spin-machine</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 72rem; margin: 0 auto; padding: 1rem; }
table { border-collapse: collapse; display: block; overflow-x: auto; }
th, td { border: 1px solid #ccc; padding: 0.3rem 0.5rem; vertical-align: top; text-align: left; }
code { font-size: 0.9em; }
</style>
</head>
<body>
<p><a href="./">Releases</a> · <a href="log.html">Log</a> · <a href="experiments.html">Experiments</a></p>
{{.Body}}
</body>
</html>
`))

// HTML is markdown, GitHub's flavour, as a web page titled title.
func HTML(title, markdown string) (string, error) {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	var body bytes.Buffer
	if err := md.Convert([]byte(markdown), &body); err != nil {
		return "", fmt.Errorf("rendering %s: %w", title, err)
	}
	var out bytes.Buffer
	if err := page.Execute(&out, struct {
		Title string
		Body  template.HTML
	}{title, template.HTML(body.String())}); err != nil { //nolint:gosec // goldmark's output of the repository's own prose
		return "", fmt.Errorf("rendering %s: %w", title, err)
	}
	return out.String(), nil
}
