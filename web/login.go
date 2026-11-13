package web

import (
	"bytes"
	"log"
	"net/http"
	"strings"
)

type loginView struct {
	Next, Error string
}

// safeNext keeps the post-sign-in redirect on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || strings.HasPrefix(next, "/login") {
		return "/failures"
	}
	return next
}

func (d *dashboard) login(w http.ResponseWriter, r *http.Request) {
	view := loginView{Next: safeNext(r.URL.Query().Get("next"))}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			d.problem(w, r, http.StatusBadRequest, "Invalid request", "Reload the page and try again.")
			return
		}
		view.Next = safeNext(r.PostForm.Get("next"))
		if d.settings.SignIn(w, r, strings.TrimSpace(r.PostForm.Get("token"))) {
			http.Redirect(w, r, view.Next, http.StatusSeeOther)
			return
		}
		view.Error, status = "That token isn't valid. Use the access token or a read token.", http.StatusUnauthorized
	}
	var body bytes.Buffer
	if err := d.templates.ExecuteTemplate(&body, "login", view); err != nil {
		log.Printf("render sign-in page: %v", err)
		http.Error(w, "Dashboard rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (d *dashboard) logout(w http.ResponseWriter, r *http.Request) {
	d.settings.SignOut(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
