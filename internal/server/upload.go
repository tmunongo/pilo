package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pilo/internal/deploy"
	"pilo/internal/store"
)

func (h *Handlers) UploadForm(w http.ResponseWriter, r *http.Request) {
	var p *store.Project
	if slug := r.PathValue("slug"); slug != "" {
		p = h.Store.GetForUser(slug, userFromContext(r.Context()).ID)
		if p == nil {
			h.NotFound(w, r)
			return
		}
		if !p.IsUpload() {
			http.Error(w, "this project deploys from Git", http.StatusBadRequest)
			return
		}
	}
	form := map[string]string{}
	if p != nil {
		form["subdir"] = p.Subdir
	}
	h.renderUpload(w, r, p, form, "", http.StatusOK)
}

func (h *Handlers) renderUpload(w http.ResponseWriter, r *http.Request, p *store.Project, form map[string]string, message string, status int) {
	action := "/uploads"
	if p != nil {
		action = "/projects/" + p.Slug + "/upload"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	render(w, h.Pages["upload"], h.commonData(r, "", map[string]any{"Project": p, "Form": form, "Error": message, "UploadAction": action}))
}

func (h *Handlers) UploadProject(w http.ResponseWriter, r *http.Request) {
	userID := userFromContext(r.Context()).ID
	slug := r.PathValue("slug")
	var p *store.Project
	if slug != "" {
		p = h.Store.GetForUser(slug, userID)
		if p == nil {
			h.NotFound(w, r)
			return
		}
		if !p.IsUpload() {
			http.Error(w, "this project deploys from Git", http.StatusBadRequest)
			return
		}
	}
	form := map[string]string{}
	fail := func(err error, code int) {
		message := err.Error()
		if p != nil && p.LastCommit != "" {
			message += " Your existing site is still live."
		}
		h.renderUpload(w, r, p, form, message, code)
	}
	select {
	case h.uploadSlots <- struct{}{}:
		defer func() { <-h.uploadSlots }()
	default:
		fail(fmt.Errorf("upload capacity is busy; try again shortly"), http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), deploy.UploadTimeout)
	defer cancel()
	// Bound both slow request reads and extraction, and reset keep-alive deadlines.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(deploy.UploadTimeout))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, deploy.MaxUploadSize+(64<<10))
	defer r.Body.Close()
	err := r.ParseMultipartForm(1 << 20)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			fail(fmt.Errorf("upload exceeds the 50 MiB limit"), http.StatusRequestEntityTooLarge)
		} else {
			fail(fmt.Errorf("could not read upload; choose one HTML file or valid ZIP archive"), http.StatusBadRequest)
		}
		return
	}
	form["slug"] = strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	form["subdir"] = strings.TrimSpace(r.FormValue("subdir"))
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		fail(fmt.Errorf("choose exactly one HTML file or ZIP archive"), http.StatusBadRequest)
		return
	}
	if slug == "" {
		slug = form["slug"]
	}
	if !slugPattern.MatchString(slug) || IsReservedSlug(slug) {
		fail(fmt.Errorf("choose a project slug using lowercase letters, numbers, and hyphens; reserved names are unavailable"), http.StatusBadRequest)
		return
	}
	unlock := h.Deployer.Lock(userID + "/" + slug)
	defer unlock()
	existing := h.Store.GetForUser(slug, userID)
	if p == nil && existing != nil {
		fail(fmt.Errorf("a project with slug %q already exists", slug), http.StatusConflict)
		return
	}
	if p != nil {
		if existing == nil || existing.ID != p.ID || !existing.IsUpload() {
			h.NotFound(w, r)
			return
		}
		p = existing
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(fmt.Errorf("could not read uploaded file"), http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size > deploy.MaxUploadSize {
		fail(fmt.Errorf("upload exceeds the 50 MiB limit"), http.StatusRequestEntityTooLarge)
		return
	}
	prepared, err := h.Deployer.PrepareUpload(ctx, userID+"/"+slug, header.Filename, form["subdir"], file, header.Size)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			fail(fmt.Errorf("upload timed out or was cancelled; try a smaller archive"), http.StatusRequestTimeout)
		} else {
			fail(err, http.StatusBadRequest)
		}
		return
	}
	defer prepared.Close()
	if err = ctx.Err(); err != nil {
		fail(fmt.Errorf("upload timed out or was cancelled; try a smaller archive"), http.StatusRequestTimeout)
		return
	}
	created := p == nil
	if created {
		p = &store.Project{Slug: slug, UserID: userID, Namespace: h.Store.Namespace(userID), SourceType: "upload", Status: store.StatusCloning}
	}
	// Ensure the project record exists before switching its filesystem pointer.
	if err = h.Store.Put(p); err != nil {
		if created {
			p = nil
		}
		fail(fmt.Errorf("could not save project; try again"), http.StatusInternalServerError)
		return
	}
	digest, err := prepared.Publish()
	if err != nil {
		if created {
			_ = h.Store.DeleteForUser(slug, userID)
			p = nil
		}
		fail(fmt.Errorf("could not publish upload; try again"), http.StatusInternalServerError)
		log.Printf("upload: publish failed slug=%s error=%v", slug, err)
		return
	}
	p.Status, p.LastCommit, p.LastError, p.Subdir = store.StatusLive, digest, "", prepared.Subdir()
	h.Router.RegisterProject(p.CheckoutKey(), h.Deployer.SiteHandler(p.CheckoutKey()))
	if err = h.Store.Put(p); err != nil {
		log.Printf("upload: published but database update failed slug=%s error=%v", slug, err)
		h.renderUpload(w, r, p, form, "Your site was published, but its project details could not be saved. Reload the project page before retrying.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/projects/"+slug, http.StatusSeeOther)
}
