// Package web embeds Pilo's admin UI templates directly into the binary,
// so deploying Pilo is just "copy one binary" — no accompanying template
// directory to keep in sync on the Pi.
package web

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

// pageFiles lists which page template pairs with which page name.
// Each page gets its own *template.Template built from base.html +
// partials.html + its own file. They're built separately (rather than
// one shared template.Template for everything) because every page
// defines its own {{define "content"}} and {{define "title"}} blocks —
// parsing them all into one set would cause silent name collisions.
var pageFiles = map[string]string{
	"index":    "templates/index.html",
	"new":      "templates/new.html",
	"project":  "templates/project.html",
	"settings": "templates/settings.html",
}

// Templates parses the embedded templates and returns one
// *template.Template per page, keyed by page name.
func Templates() map[string]*template.Template {
	out := make(map[string]*template.Template, len(pageFiles))
	for name, file := range pageFiles {
		out[name] = template.Must(template.ParseFS(templateFS,
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
	return template.Must(template.ParseFS(templateFS, "templates/partials.html"))
}
