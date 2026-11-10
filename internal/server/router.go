package server

import (
	"net/http"
	"strings"
	"sync"
)

// reservedSlugs can never be used as a project slug, since they'd
// collide with Pilo's own admin routes.
var reservedSlugs = map[string]bool{
	"":                true,
	"projects":        true,
	"webhooks":        true,
	"static":          true,
	"health":          true,
	"settings":        true,
	"pilo":            true,
	"login":           true,
	"logout":          true,
	"signup":          true,
	"register":        true,
	"forgot-password": true,
	"reset-password":  true,
}

// IsReservedSlug reports whether slug is reserved for Pilo's own use.
func IsReservedSlug(slug string) bool {
	return reservedSlugs[strings.ToLower(slug)]
}

// Router dispatches requests to dynamically-registered project handlers
// (matched by owner and slug) and falls back to the admin mux
// for everything else. Routes can be added/removed at runtime, which is
// the whole reason this isn't just a plain http.ServeMux — the stdlib
// mux doesn't support re-registering or removing patterns after the
// fact.
type Router struct {
	mu              sync.RWMutex
	projects        map[string]http.Handler // userID/slug -> handler
	namespaceUserID func(string) string
	admin           http.Handler
}

func NewRouter(admin http.Handler) *Router {
	return &Router{
		projects: make(map[string]http.Handler),
		admin:    admin,
	}
}

// SetAdmin attaches (or replaces) the fallback admin handler. Needed
// because Handlers requires a *Router to build its mux, and Router's
// admin handler is that same mux — so it's wired up in two steps.
func (r *Router) SetAdmin(admin http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin = admin
}

// RegisterProject registers a stable userID/slug checkout key.
func (r *Router) RegisterProject(slug string, handler http.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[slug] = handler
}

// UnregisterProject removes a previously-registered project route.
func (r *Router) UnregisterProject(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.projects, slug)
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/", 4)
	r.mu.RLock()
	resolver := r.namespaceUserID
	admin := r.admin
	r.mu.RUnlock()
	if len(parts) >= 3 && parts[0] == "projects" && resolver != nil {
		userID := resolver(parts[1])
		r.mu.RLock()
		handler, ok := r.projects[userID+"/"+parts[2]]
		r.mu.RUnlock()
		if userID != "" && ok {
			prefix := "/projects/" + parts[1] + "/" + parts[2]
			// Keep exact management endpoints available even for sites named edit/redeploy.
			if req.URL.Path == prefix && (parts[2] == "edit" || parts[2] == "redeploy") {
				admin.ServeHTTP(w, req)
				return
			}
			w.Header().Set("Cache-Control", "public, no-cache")
			if req.URL.Path == prefix {
				http.Redirect(w, req, prefix+"/"+querySuffix(req), http.StatusMovedPermanently)
				return
			}
			http.StripPrefix(prefix, handler).ServeHTTP(w, req)
			return
		}
	}
	admin.ServeHTTP(w, req)
}

func querySuffix(r *http.Request) string {
	if r.URL.RawQuery != "" {
		return "?" + r.URL.RawQuery
	}
	return ""
}

func (r *Router) SetNamespaceResolver(resolve func(string) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.namespaceUserID = resolve
}
