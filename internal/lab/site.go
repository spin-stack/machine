// SPDX-License-Identifier: Apache-2.0

package lab

import (
	_ "embed"
	"fmt"
	"html"
	"html/template"
	"strings"
)

// SiteRecords is the directory beside the site's page that holds each record: the page links a
// release to SiteRecords/RELEASE.json, and releases site writes them there.
const SiteRecords = "releases"

//go:embed site.html
var siteHTML string

// site is the page's template: html/template, so that every release, host and reason a record
// holds is escaped wherever it lands.
var site = template.Must(template.New("site").Parse(siteHTML))

// Site is the records' web page (releases site, pages.yml): what Doc says, from the same tables,
// as one HTML file that fetches nothing, the chart inline so that its points' tooltips show.
func Site(records []Record) (string, error) {
	var b strings.Builder
	err := site.Execute(&b, struct {
		About  template.HTML
		Chart  template.HTML
		Tables []table
	}{
		About:  prose(about()),
		Chart:  template.HTML(Chart(records)), //nolint:gosec // Chart escapes every value it writes
		Tables: tables(records, SiteRecords),
	})
	if err != nil {
		return "", fmt.Errorf("writing the records' page: %w", err)
	}
	return b.String(), nil
}

// prose is the page's prose as HTML: escaped, its `code` as code. It holds no other Markdown.
func prose(s string) template.HTML {
	var b strings.Builder
	for i, part := range strings.Split(s, "`") {
		if i%2 == 1 {
			b.WriteString("<code>" + html.EscapeString(part) + "</code>")
		} else {
			b.WriteString(html.EscapeString(part))
		}
	}
	return template.HTML(b.String()) //nolint:gosec // every part escaped above
}
