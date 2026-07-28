package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"pilo/internal/deploy"
	"pilo/internal/store"
	"pilo/web"
)

// Handlers holds everything the admin UI needs to do its job.
type Handlers struct {
	Store    *store.Store
	Deployer *deploy.Deployer
	Router   *Router
	Pages    map[string]*template.Template
	Partials *template.Template
	BaseURL  string // used to render the webhook URL on the settings page
}

func NewHandlers(s *store.Store, d *deploy.Deployer, r *Router, baseURL string) *Handlers {
	return &Handlers{
		Store:    s,
		Deployer: d,
		Router:   r,
		Pages:    web.Templates(),
		Partials: web.PartialsTemplate(),
		BaseURL:  baseURL,
	}
}

// Mux builds the admin http.Handler with all routes registered.
func (h *Handlers) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.Index)
	mux.HandleFunc("GET /projects/new", h.NewForm)
	mux.HandleFunc("POST /projects", h.Create)
	mux.HandleFunc("GET /projects/{slug}", h.Detail)
	mux.HandleFunc("POST /projects/{slug}/redeploy", h.Redeploy)
	mux.HandleFunc("DELETE /projects/{slug}", h.Delete)
	mux.HandleFunc("GET /settings", h.SettingsForm)
	mux.HandleFunc("POST /settings", h.SettingsSave)
	mux.HandleFunc("GET /health", h.Health)
	return mux
}

func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["index"], map[string]any{
		"Projects": h.Store.All(),
	})
}

func (h *Handlers) NewForm(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["new"], map[string]any{
		"Form": map[string]string{},
	})
}

var repoURLPattern = regexp.MustCompile(`github\.com[:/]([\w.-]+)/([\w.-]+?)(\.git)?/?$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// parseRepo accepts "owner/repo", a full https URL, or an ssh-style
// git@github.com:owner/repo.git reference and extracts owner/repo.
func parseRepo(input string) (owner, name string, err error) {
	input = strings.TrimSpace(input)
	if m := repoURLPattern.FindStringSubmatch(input); m != nil {
		return m[1], m[2], nil
	}
	if parts := strings.Split(input, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
	}
	return "", "", fmt.Errorf("couldn't parse %q as owner/repo", input)
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	form := map[string]string{
		"repo":   r.FormValue("repo"),
		"slug":   r.FormValue("slug"),
		"branch": r.FormValue("branch"),
		"subdir": r.FormValue("subdir"),
	}

	fail := func(msg string) {
		render(w, h.Pages["new"], map[string]any{"Error": msg, "Form": form})
	}

	owner, name, err := parseRepo(form["repo"])
	if err != nil {
		fail(err.Error())
		return
	}

	slug := strings.ToLower(strings.TrimSpace(form["slug"]))
	if slug == "" {
		slug = strings.ToLower(name)
	}
	if !slugPattern.MatchString(slug) {
		fail("slug must be lowercase letters, numbers, and hyphens only")
		return
	}
	if IsReservedSlug(slug) {
		fail(fmt.Sprintf("%q is reserved and can't be used as a project slug", slug))
		return
	}
	if h.Store.Get(slug) != nil {
		fail(fmt.Sprintf("a project with slug %q already exists", slug))
		return
	}

	branch := strings.TrimSpace(form["branch"])
	if branch == "" {
		branch = "main"
	}

	p := &store.Project{
		ID:        slug,
		Slug:      slug,
		RepoOwner: owner,
		RepoName:  name,
		Branch:    branch,
		Subdir:    strings.TrimSpace(form["subdir"]),
		Status:    store.StatusCloning,
		CreatedAt: time.Now(),
	}
	if err := h.Store.Put(p); err != nil {
		fail("failed to save project: " + err.Error())
		return
	}

	h.deployAndRegister(p)

	http.Redirect(w, r, "/projects/"+slug, http.StatusSeeOther)
}

// deployAndRegister clones (or pulls) a project's repo, updates its
// status in the store, and — on success — registers its route with the
// live router so it's immediately reachable.
func (h *Handlers) deployAndRegister(p *store.Project) {
	commit, err := h.Deployer.Clone(p.Slug, p.RepoURL(), p.Branch, h.Store.GitHubToken)
	if err != nil {
		p.Status = store.StatusError
		p.LastError = err.Error()
		log.Printf("deploy %s: %v", p.Slug, err)
		_ = h.Store.Put(p)
		return
	}

	p.Status = store.StatusLive
	p.LastCommit = commit
	p.LastError = ""
	_ = h.Store.Put(p)

	root := h.Deployer.PathFor(p.Slug)
	if p.Subdir != "" {
		root = root + "/" + p.Subdir
	}
	h.Router.RegisterProject(p.Slug, http.FileServer(http.Dir(root)))
}

func (h *Handlers) Detail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := h.Store.Get(slug)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	render(w, h.Pages["project"], map[string]any{"Project": p})
}

func (h *Handlers) Redeploy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p := h.Store.Get(slug)
	if p == nil {
		http.NotFound(w, r)
		return
	}

	commit, err := h.Deployer.Pull(slug, p.Branch, h.Store.GitHubToken)
	if err != nil {
		p.Status = store.StatusError
		p.LastError = err.Error()
	} else {
		p.Status = store.StatusLive
		p.LastCommit = commit
		p.LastError = ""
	}
	_ = h.Store.Put(p)

	renderPartial(w, h.Partials, "project_row", p)
}

func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.Store.Get(slug) == nil {
		http.NotFound(w, r)
		return
	}
	h.Router.UnregisterProject(slug)
	_ = h.Deployer.Remove(slug)
	_ = h.Store.Delete(slug)

	// htmx target is "body" for this one — send back the refreshed list
	// wrapped in a full page so the redirect-free swap still looks right.
	w.Header().Set("HX-Redirect", "/")
}

func (h *Handlers) SettingsForm(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["settings"], map[string]any{
		"HasToken":   h.Store.GitHubToken != "",
		"HasSecret":  h.Store.WebhookSecret != "",
		"WebhookURL": h.BaseURL + "/webhooks/github",
	})
}

func (h *Handlers) SettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if tok := r.FormValue("github_token"); tok != "" {
		_ = h.Store.SetGitHubToken(tok)
	}
	if sec := r.FormValue("webhook_secret"); sec != "" {
		_ = h.Store.SetWebhookSecret(sec)
	}
	render(w, h.Pages["settings"], map[string]any{
		"Saved":      true,
		"HasToken":   h.Store.GitHubToken != "",
		"HasSecret":  h.Store.WebhookSecret != "",
		"WebhookURL": h.BaseURL + "/webhooks/github",
	})
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}
