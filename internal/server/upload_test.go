package server_test

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"os"
	"pilo/internal/deploy"
	"pilo/internal/server"
	"pilo/internal/store"
)

func uploadRequest(t *testing.T, router http.Handler, cookie *http.Cookie, target, slug, subdir, filename string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if err := w.WriteField("slug", slug); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("subdir", subdir); err != nil {
		t.Fatal(err)
	}
	f, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", target, &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func uploadZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestUploadProjectEndToEnd(t *testing.T) {
	st, router, h := setupTestServer(t)
	u, err := st.CreateUser("uploader@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := st.CreateSession(u.ID, time.Hour)
	cookie := &http.Cookie{Name: "pilo_session", Value: sess.ID}
	request := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	readSite := func(target, want string, code int) {
		t.Helper()
		rec := request("GET", target)
		if rec.Code != code || (want != "" && rec.Body.String() != want) {
			t.Fatalf("site %s: %d %q", target, rec.Code, rec.Body.String())
		}
	}
	// Missing entry pages give an inline error and never create a project.
	rec := uploadRequest(t, router, cookie, "/uploads", "demo", "", "site.zip", uploadZIP(t, map[string]string{"readme": "docs"}))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "no index.html") || st.GetForUser("demo", u.ID) != nil {
		t.Fatalf("invalid ZIP: %d %s", rec.Code, rec.Body.String())
	}
	rec = uploadRequest(t, router, cookie, "/uploads", "demo", "", "my-page.html", []byte("<h1>first</h1>"))
	if rec.Code != 303 {
		t.Fatalf("HTML upload: %d %s", rec.Code, rec.Body.String())
	}
	p := st.GetForUser("demo", u.ID)
	if p == nil || !p.IsUpload() || len(p.LastCommit) != 64 || p.Status != store.StatusLive {
		t.Fatalf("project not persisted: %+v", p)
	}
	old := h.Deployer.Commits(p.CheckoutKey(), 0)[0]
	readSite(p.PublicPath(), "<h1>first</h1>", 200)
	rec = request("GET", "/projects/demo")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "upload new version") || strings.Contains(rec.Body.String(), "automatic deployments") || strings.Contains(rec.Body.String(), ">repo</dt>") {
		t.Fatal("wrong upload detail UI")
	}
	// Replacement is a complete snapshot, and root detection survives restarts.
	rec = uploadRequest(t, router, cookie, "/projects/demo/upload", "", "", "site.zip", uploadZIP(t, map[string]string{"site/index.html": "second", "site/asset.css": "css"}))
	if rec.Code != 303 {
		t.Fatalf("ZIP upload: %d %s", rec.Code, rec.Body.String())
	}
	p = st.GetForUser("demo", u.ID)
	if p.Subdir != "site" {
		t.Fatal(p.Subdir)
	}
	readSite(p.PublicPath(), "second", 200)
	readSite(p.PublicPath()+"asset.css", "css", 200)
	if err = h.Deployer.MigrateReleases(st.All()); err != nil {
		t.Fatal(err)
	}
	restarted := server.NewRouter(nil)
	restartHandlers := server.NewHandlers(st, h.Deployer, restarted, "http://localhost:8080")
	restarted.SetAdmin(restartHandlers.Mux())
	restarted.RegisterProject(p.CheckoutKey(), h.Deployer.SiteHandler(p.CheckoutKey()))
	check := httptest.NewRecorder()
	restarted.ServeHTTP(check, httptest.NewRequest("GET", p.PublicPath(), nil))
	if check.Body.String() != "second" {
		t.Fatal("restart lost upload")
	}
	// Failed replacement keeps the live pointer, status, and history unchanged.
	for _, raw := range [][]byte{[]byte("not a zip"), uploadZIP(t, map[string]string{"index.html": "evil", "../escape": "bad"}), uploadZIP(t, map[string]string{"wrong.txt": "no index"})} {
		rec = uploadRequest(t, router, cookie, "/projects/demo/upload", "", "site", "bad.zip", raw)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Your existing site is still live.") {
			t.Fatalf("error UI: %d %s", rec.Code, rec.Body.String())
		}
		readSite(p.PublicPath(), "second", 200)
	}
	if len(h.Deployer.Commits(p.CheckoutKey(), 0)) != 2 {
		t.Fatal("invalid uploads changed history")
	}
	// Return from a nested ZIP root to a single HTML file, dropping old assets.
	rec = uploadRequest(t, router, cookie, "/projects/demo/upload", "", "", "page.htm", []byte("third"))
	if rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	readSite(p.PublicPath(), "third", 200)
	readSite(p.PublicPath()+"asset.css", "", 404)
	values := url.Values{"release": {old.ID}}
	req := httptest.NewRequest("POST", "/projects/demo/rollback", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	readSite(p.PublicPath(), "<h1>first</h1>", 200)
	rec = request("POST", "/projects/demo/redeploy")
	if rec.Code != 303 || rec.Header().Get("Location") != "/projects/demo/upload" {
		t.Fatal("upload attempted Git redeploy")
	}
	rec = request("GET", "/dashboard")
	if !strings.Contains(rec.Body.String(), "HTML / ZIP upload") {
		t.Fatal("dashboard omits upload source")
	}
}

func TestUploadAuthenticationIsolationAndValidation(t *testing.T) {
	st, router, _ := setupTestServer(t)
	one, _ := st.CreateUser("one-upload@example.com", "password123")
	two, _ := st.CreateUser("two-upload@example.com", "password123")
	a, _ := st.CreateSession(one.ID, time.Hour)
	b, _ := st.CreateSession(two.ID, time.Hour)
	ca := &http.Cookie{Name: "pilo_session", Value: a.ID}
	cb := &http.Cookie{Name: "pilo_session", Value: b.ID}
	if rec := uploadRequest(t, router, nil, "/uploads", "demo", "", "a.html", []byte("one")); rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatal("anonymous upload allowed")
	}
	if rec := uploadRequest(t, router, ca, "/uploads", "demo", "", "a.html", []byte("one")); rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if rec := uploadRequest(t, router, cb, "/projects/demo/upload", "", "", "a.html", []byte("two")); rec.Code != 404 {
		t.Fatal("cross-owner upload allowed")
	}
	if rec := uploadRequest(t, router, cb, "/uploads", "demo", "", "a.html", []byte("two")); rec.Code != 303 {
		t.Fatal("account slug isolation failed")
	}
	if rec := uploadRequest(t, router, ca, "/uploads", "demo", "", "a.html", []byte("duplicate")); rec.Code != 409 {
		t.Fatal("duplicate project accepted")
	}
	for _, tc := range []struct{ slug, subdir, filename string }{{"bad/path", "", "a.html"}, {"settings", "", "a.html"}, {"other", "../escape", "a.zip"}, {"other", "", "a.exe"}} {
		if rec := uploadRequest(t, router, ca, "/uploads", tc.slug, tc.subdir, tc.filename, []byte("bad")); rec.Code != 400 {
			t.Fatal("invalid input accepted", tc, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/uploads", strings.NewReader("bad"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(ca)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross-origin upload allowed")
	}
	if err := st.Put(&store.Project{Slug: "git-source", UserID: one.ID, RepoOwner: "owner", RepoName: "repo"}); err != nil {
		t.Fatal(err)
	}
	if p := st.GetForUser("git-source", one.ID); p.SourceType != "git" {
		t.Fatal("legacy source default changed")
	}
	if rec := uploadRequest(t, router, ca, "/projects/git-source/upload", "", "", "a.html", []byte("bad")); rec.Code != 400 {
		t.Fatal("Git source replaced by upload")
	}
}

func TestUploadRequestSizeLimit(t *testing.T) {
	st, router, _ := setupTestServer(t)
	user, _ := st.CreateUser("large-upload@example.com", "password123")
	sess, _ := st.CreateSession(user.ID, time.Hour)
	// Stream the oversized body without allocating it in memory.
	const boundary = "pilo-test-boundary"
	prefix := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"huge.zip\"\r\nContent-Type: application/zip\r\n\r\n"
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	body := io.MultiReader(strings.NewReader(prefix), io.LimitReader(uploadZeros{}, deploy.MaxUploadSize+(128<<10)))
	req := httptest.NewRequest("POST", "/uploads", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 413 || !strings.Contains(rec.Body.String(), "50 MiB") {
		t.Fatalf("oversized upload: %d %s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("multipart temporary files leaked", entries, err)
	}
}

type uploadZeros struct{}

func (uploadZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }
