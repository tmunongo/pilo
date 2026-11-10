// Package web embeds Pilo's admin UI templates directly into the binary,
// so deploying Pilo is just "copy one binary" — no accompanying template
// directory to keep in sync on the Pi.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Reltime formats a time as a human-readable relative string.
func Reltime(t time.Time) string {
	return reltime(t)
}

// reltime formats a time as a human-readable relative string.
// e.g. "just now", "12 s ago", "4 min ago", "3 days ago", "2 weeks ago".
func reltime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < 30*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		mins := int(d.Minutes())
		return fmt.Sprintf("%d min ago", mins)
	case d < 24*time.Hour:
		hrs := int(d.Hours())
		if hrs == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hrs)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		weeks := int(d.Hours() / (24 * 7))
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}
}

var funcMap = template.FuncMap{
	"reltime": reltime,
	"isFresh": func(t time.Time) bool {
		if t.IsZero() {
			return false
		}
		return time.Since(t) <= 15*time.Minute
	},
	// slice returns s[low:high] — useful for trimming commit SHAs in templates.
	"slice": func(s string, low, high int) string {
		if low < 0 {
			low = 0
		}
		if low >= len(s) {
			return ""
		}
		if high > len(s) {
			high = len(s)
		}
		return s[low:high]
	},
}

// pageFiles lists which page template pairs with which page name.
// Each page gets its own *template.Template built from base.html +
// partials.html + its own file. They're built separately (rather than
// one shared template.Template for everything) because every page
// defines its own {{define "content"}} and {{define "title"}} blocks —
// parsing them all into one set would cause silent name collisions.
var pageFiles = map[string]string{
	"landing":         "templates/landing.html",
	"not_found":       "templates/not_found.html",
	"index":           "templates/index.html",
	"new":             "templates/new.html",
	"edit":            "templates/edit.html",
	"project":         "templates/project.html",
	"settings":        "templates/settings.html",
	"login":           "templates/login.html",
	"signup":          "templates/signup.html",
	"forgot_password": "templates/forgot_password.html",
	"reset_password":  "templates/reset_password.html",
}

// Templates parses the embedded templates and returns one
// *template.Template per page, keyed by page name.
func Templates() map[string]*template.Template {
	out := make(map[string]*template.Template, len(pageFiles))
	for name, file := range pageFiles {
		out[name] = template.Must(
			template.New("").Funcs(funcMap).ParseFS(templateFS,
				"templates/base.html",
				"templates/partials.html",
				file,
			))
	}
	return out
}

// PartialsTemplate returns just the shared partials — used by handlers
// that only need to render a fragment like "project_row" for an htmx
// swap, without the rest of a full page.
func PartialsTemplate() *template.Template {
	return template.Must(
		template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/partials.html"))
}
