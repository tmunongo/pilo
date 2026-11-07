package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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
	Store     *store.Store
	Deployer  *deploy.Deployer
	Router    *Router
	Pages     map[string]*template.Template
	Partials  *template.Template
	BaseURL   string // used to render the webhook URL on the settings page
	Port      string
	SitesPath string
	Version   string
}

func NewHandlers(s *store.Store, d *deploy.Deployer, r *Router, baseURL string) *Handlers {
	sitesPath := ""
	if d != nil {
		sitesPath = d.ReposRoot
	}
	return &Handlers{
		Store:     s,
		Deployer:  d,
		Router:    r,
		Pages:     web.Templates(),
		Partials:  web.PartialsTemplate(),
		BaseURL:   baseURL,
		SitesPath: sitesPath,
	}
}

func (h *Handlers) commonData(activeNav string, extra map[string]any) map[string]any {
	data := map[string]any{
		"ActiveNav":  activeNav,
		"Port":       h.Port,
		"SitesPath":  h.SitesPath,
		"Version":    h.Version,
		"WebhookURL": h.BaseURL + "/webhooks/github",
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
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
	mux.HandleFunc("POST /webhooks/github", h.WebhookGitHub)
	mux.HandleFunc("GET /health", h.Health)
	return mux
}

func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	projects := h.Store.All()
	var building bool
	for _, p := range projects {
		if p.Status == store.StatusCloning {
			building = true
			break
		}
	}
	render(w, h.Pages["index"], h.commonData("projects", map[string]any{
		"Projects":        projects,
		"BuildingProject": building,
	}))
}

func (h *Handlers) NewForm(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["new"], h.commonData("", map[string]any{
		"Form": map[string]string{},
	}))
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
		render(w, h.Pages["new"], h.commonData("", map[string]any{
			"Error": msg,
			"Form":  form,
		}))
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
	var deploys []deploy.CommitInfo
	if h.Deployer != nil {
		deploys = h.Deployer.Commits(p.Slug, 5)
	}
	render(w, h.Pages["project"], h.commonData("", map[string]any{
		"Project": p,
		"Deploys": deploys,
	}))
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

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/projects/"+slug, http.StatusSeeOther)
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
	render(w, h.Pages["settings"], h.commonData("settings", map[string]any{
		"HasToken":  h.Store.GitHubToken != "",
		"HasSecret": h.Store.WebhookSecret != "",
	}))
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
	render(w, h.Pages["settings"], h.commonData("settings", map[string]any{
		"Saved":     true,
		"HasToken":  h.Store.GitHubToken != "",
		"HasSecret": h.Store.WebhookSecret != "",
	}))
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

type githubWebhookPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
			Name  string `json:"name"`
		} `json:"owner"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *Handlers) WebhookGitHub(w http.ResponseWriter, r *http.Request) {
	secret := h.Store.WebhookSecret
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(secret, body, sig) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
	}

	var payload githubWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	owner := payload.Repository.Owner.Login
	if owner == "" {
		owner = payload.Repository.Owner.Name
	}
	repo := payload.Repository.Name

	if owner == "" || repo == "" || branch == "" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ignored: missing repository or branch details"))
		return
	}

	redeployedCount := 0
	for _, p := range h.Store.All() {
		if strings.EqualFold(p.RepoOwner, owner) && strings.EqualFold(p.RepoName, repo) && p.Branch == branch {
			commit, err := h.Deployer.Pull(p.Slug, p.Branch, h.Store.GitHubToken)
			if err != nil {
				p.Status = store.StatusError
				p.LastError = err.Error()
				log.Printf("webhook redeploy %s error: %v", p.Slug, err)
			} else {
				p.Status = store.StatusLive
				p.LastCommit = commit
				p.LastError = ""
				log.Printf("webhook redeploy %s success: commit %s", p.Slug, commit)
			}
			_ = h.Store.Put(p)
			redeployedCount++
		}
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "redeployed %d project(s)", redeployedCount)
}

func verifySignature(secret string, payload []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigHex := strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sigHex), []byte(expectedSig))
}
