package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
)

// Badge writes a small shields.io-style SVG, such as "retry safety | 6/6
// held", for a README. It is generated locally, so it needs no service.
func Badge(w io.Writer, audits []audit.Report) error {
	s := audit.Combine(audits)
	label := "retry safety"
	value := fmt.Sprintf("%d/%d held", s.Held, s.Ran())
	color := map[string]string{"PASS": "#2ea44f", "VIOLATION": "#d73a49", "ERROR": "#dbab09"}[s.Status]
	if s.Status == "ERROR" && s.Violations == 0 && s.Held == 0 {
		value = "inconclusive"
	}
	lw, vw := textWidth(label)+12, textWidth(value)+12
	total := lw + vw
	_, err := fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">
  <title>%s: %s</title>
  <linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>
  <clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>
  <g clip-path="url(#r)">
    <rect width="%d" height="20" fill="#555"/>
    <rect x="%d" width="%d" height="20" fill="%s"/>
    <rect width="%d" height="20" fill="url(#s)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>
`, total, esc(label), esc(value), esc(label), esc(value),
		total, lw, lw, vw, color, total,
		lw/2, esc(label), lw/2, esc(label),
		lw+vw/2, esc(value), lw+vw/2, esc(value))
	return err
}

var attr = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func esc(s string) string { return attr.Replace(s) }

// textWidth approximates Verdana 11px, which is what badge viewers expect.
func textWidth(s string) int {
	w := 0.0
	for _, r := range s {
		switch {
		case r == ' ':
			w += 3.9
		case r == '/' || r == 'i' || r == 'l' || r == 'f' || r == 't' || r == 'r':
			w += 4.4
		case r >= '0' && r <= '9':
			w += 7.0
		case r == 'm' || r == 'w':
			w += 10.7
		default:
			w += 6.8
		}
	}
	return int(w + 0.5)
}
