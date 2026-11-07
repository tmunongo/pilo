package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type contextKey string

const (
	userContextKey    contextKey = "pilo_user"
	sessionCookieName            = "pilo_session"
)

// Handlers holds everything the admin UI needs to do its job.
type Handlers struct {
	Store     *store.Store
	Deployer  *deploy.Deployer
	Router    *Router
	Mailer    *Mailer
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
		Mailer:    NewMailerFromEnv(),
		Pages:     web.Templates(),
		Partials:  web.PartialsTemplate(),
		BaseURL:   baseURL,
		SitesPath: sitesPath,
	}
}

func withUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

func userFromContext(ctx context.Context) *store.User {
	if u, ok := ctx.Value(userContextKey).(*store.User); ok {
		return u
	}
	return nil
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		MaxAge:   int((30 * 24 * time.Hour) / time.Second),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func (h *Handlers) commonData(r *http.Request, activeNav string, extra map[string]any) map[string]any {
	data := map[string]any{
		"User":       userFromContext(r.Context()),
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

	// Public routes
	mux.HandleFunc("GET /login", h.LoginForm)
	mux.HandleFunc("POST /login", h.Login)
	mux.HandleFunc("GET /signup", h.SignUpForm)
	mux.HandleFunc("POST /signup", h.SignUp)
	mux.HandleFunc("POST /logout", h.Logout)
	mux.HandleFunc("GET /logout", h.Logout)
	mux.HandleFunc("GET /forgot-password", h.ForgotPasswordForm)
	mux.HandleFunc("POST /forgot-password", h.ForgotPassword)
	mux.HandleFunc("GET /reset-password", h.ResetPasswordForm)
	mux.HandleFunc("POST /reset-password", h.ResetPassword)
	mux.HandleFunc("POST /webhooks/github", h.WebhookGitHub)
	mux.HandleFunc("GET /health", h.Health)

	// Protected routes
	mux.Handle("GET /{$}", h.requireAuth(http.HandlerFunc(h.Index)))
	mux.Handle("GET /projects/new", h.requireAuth(http.HandlerFunc(h.NewForm)))
	mux.Handle("POST /projects", h.requireAuth(http.HandlerFunc(h.Create)))
	mux.Handle("GET /projects/{slug}", h.requireAuth(http.HandlerFunc(h.Detail)))
	mux.Handle("POST /projects/{slug}/redeploy", h.requireAuth(http.HandlerFunc(h.Redeploy)))
	mux.Handle("DELETE /projects/{slug}", h.requireAuth(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /settings", h.requireAuth(http.HandlerFunc(h.SettingsForm)))
	mux.Handle("POST /settings", h.requireAuth(http.HandlerFunc(h.SettingsSave)))

	return h.loadUserMiddleware(mux)
}

func (h *Handlers) loadUserMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" {
			if u, err := h.Store.GetSessionUser(cookie.Value); err == nil && u != nil {
				r = r.WithContext(withUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFromContext(r.Context())
		if u == nil {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Auth handlers --------------------------------------------------------

func (h *Handlers) LoginForm(w http.ResponseWriter, r *http.Request) {
	if userFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	render(w, h.Pages["login"], h.commonData(r, "login", map[string]any{
		"Form":   map[string]string{},
		"Notice": r.URL.Query().Get("notice"),
	}))
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")

	u, err := h.Store.AuthenticateUser(email, password)
	if err != nil {
		render(w, h.Pages["login"], h.commonData(r, "login", map[string]any{
			"Error": "Invalid email or password",
			"Form":  map[string]string{"email": email},
		}))
		return
	}

	sess, err := h.Store.CreateSession(u.ID, 30*24*time.Hour)
	if err != nil {
		render(w, h.Pages["login"], h.commonData(r, "login", map[string]any{
			"Error": "Failed to create session: " + err.Error(),
			"Form":  map[string]string{"email": email},
		}))
		return
	}

	setSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) SignUpForm(w http.ResponseWriter, r *http.Request) {
	if userFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	render(w, h.Pages["signup"], h.commonData(r, "", map[string]any{
		"Form": map[string]string{},
	}))
}

func (h *Handlers) SignUp(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")

	fail := func(msg string) {
		render(w, h.Pages["signup"], h.commonData(r, "", map[string]any{
			"Error": msg,
			"Form":  map[string]string{"email": email},
		}))
	}

	if password != confirm {
		fail("Passwords do not match")
		return
	}
	if len(password) < 8 {
		fail("Password must be at least 8 characters long")
		return
	}

	u, err := h.Store.CreateUser(email, password)
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			fail("An account with that email already exists")
			return
		}
		fail(err.Error())
		return
	}

	sess, err := h.Store.CreateSession(u.ID, 30*24*time.Hour)
	if err != nil {
		fail("Account created but failed to create session: " + err.Error())
		return
	}

	setSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		_ = h.Store.DeleteSession(cookie.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handlers) ForgotPasswordForm(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["forgot_password"], h.commonData(r, "", map[string]any{
		"Form": map[string]string{},
	}))
}

func (h *Handlers) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))

	rawToken, _, err := h.Store.CreatePasswordReset(email, 1*time.Hour)
	if err == nil {
		resetURL := fmt.Sprintf("%s/reset-password?token=%s", h.BaseURL, rawToken)
		_ = h.Mailer.SendPasswordReset(email, resetURL)
	}

	render(w, h.Pages["forgot_password"], h.commonData(r, "", map[string]any{
		"Success": "If an account exists for that email, we've sent password reset instructions.",
		"Form":    map[string]string{"email": email},
	}))
}

func (h *Handlers) ResetPasswordForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		render(w, h.Pages["reset_password"], h.commonData(r, "", map[string]any{
			"InvalidToken": true,
		}))
		return
	}

	if _, err := h.Store.VerifyPasswordReset(token); err != nil {
		render(w, h.Pages["reset_password"], h.commonData(r, "", map[string]any{
			"InvalidToken": true,
		}))
		return
	}

	render(w, h.Pages["reset_password"], h.commonData(r, "", map[string]any{
		"Token": token,
	}))
}

func (h *Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")

	fail := func(msg string) {
		render(w, h.Pages["reset_password"], h.commonData(r, "", map[string]any{
			"Token": token,
			"Error": msg,
		}))
	}

	if password != confirm {
		fail("Passwords do not match")
		return
	}
	if len(password) < 8 {
		fail("Password must be at least 8 characters long")
		return
	}

	if err := h.Store.ResetPasswordWithToken(token, password); err != nil {
		if errors.Is(err, store.ErrInvalidResetToken) {
			render(w, h.Pages["reset_password"], h.commonData(r, "", map[string]any{
				"InvalidToken": true,
			}))
			return
		}
		fail(err.Error())
		return
	}

	http.Redirect(w, r, "/login?notice=Password+successfully+updated.+Please+sign+in.", http.StatusSeeOther)
}

// --- Project handlers -----------------------------------------------------

func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	projects := h.Store.AllForUser(u.ID)
	var building bool
	for _, p := range projects {
		if p.Status == store.StatusCloning {
			building = true
			break
		}
	}
	render(w, h.Pages["index"], h.commonData(r, "projects", map[string]any{
		"Projects":        projects,
		"BuildingProject": building,
	}))
}

func (h *Handlers) NewForm(w http.ResponseWriter, r *http.Request) {
	render(w, h.Pages["new"], h.commonData(r, "", map[string]any{
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
	u := userFromContext(r.Context())
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
		render(w, h.Pages["new"], h.commonData(r, "", map[string]any{
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
		UserID:    u.ID,
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
	token := h.Store.EffectiveGitHubToken(p.UserID)
	commit, err := h.Deployer.Clone(p.Slug, p.RepoURL(), p.Branch, token)
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
	u := userFromContext(r.Context())
	p := h.Store.GetForUser(slug, u.ID)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	var deploys []deploy.CommitInfo
	if h.Deployer != nil {
		deploys = h.Deployer.Commits(p.Slug, 5)
	}
	render(w, h.Pages["project"], h.commonData(r, "", map[string]any{
		"Project": p,
		"Deploys": deploys,
	}))
}

func (h *Handlers) Redeploy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	u := userFromContext(r.Context())
	p := h.Store.GetForUser(slug, u.ID)
	if p == nil {
		http.NotFound(w, r)
		return
	}

	token := h.Store.EffectiveGitHubToken(u.ID)
	commit, err := h.Deployer.Pull(slug, p.Branch, token)
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
	u := userFromContext(r.Context())
	if h.Store.GetForUser(slug, u.ID) == nil {
		http.NotFound(w, r)
		return
	}
	h.Router.UnregisterProject(slug)
	_ = h.Deployer.Remove(slug)
	_ = h.Store.DeleteForUser(slug, u.ID)

	w.Header().Set("HX-Redirect", "/")
}

func (h *Handlers) SettingsForm(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	tok, _ := h.Store.UserSetting(u.ID, "github_token")
	sec, _ := h.Store.UserSetting(u.ID, "webhook_secret")
	render(w, h.Pages["settings"], h.commonData(r, "settings", map[string]any{
		"HasToken":  tok != "",
		"HasSecret": sec != "",
	}))
}

func (h *Handlers) SettingsSave(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if tok := r.FormValue("github_token"); tok != "" {
		_ = h.Store.SetUserSetting(u.ID, "github_token", tok)
	}
	if sec := r.FormValue("webhook_secret"); sec != "" {
		_ = h.Store.SetUserSetting(u.ID, "webhook_secret", sec)
	}
	tok, _ := h.Store.UserSetting(u.ID, "github_token")
	sec, _ := h.Store.UserSetting(u.ID, "webhook_secret")
	render(w, h.Pages["settings"], h.commonData(r, "settings", map[string]any{
		"Saved":     true,
		"HasToken":  tok != "",
		"HasSecret": sec != "",
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
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

	sig := r.Header.Get("X-Hub-Signature-256")
	secret := h.Store.WebhookSecret
	if secret != "" {
		if !verifySignature(secret, body, sig) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
	}
	redeployedCount := 0
	for _, p := range h.Store.All() {
		if strings.EqualFold(p.RepoOwner, owner) && strings.EqualFold(p.RepoName, repo) && p.Branch == branch {
			secret := h.Store.EffectiveWebhookSecret(p.UserID)
			if secret != "" && !verifySignature(secret, body, sig) {
				log.Printf("webhook redeploy %s: invalid signature", p.Slug)
				continue
			}
			token := h.Store.EffectiveGitHubToken(p.UserID)
			commit, err := h.Deployer.Pull(p.Slug, p.Branch, token)
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
