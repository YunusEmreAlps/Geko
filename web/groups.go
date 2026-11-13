package web

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// changeGroupState resolves, mutes or reopens an error group, then returns to
// the page the form was submitted from.
func (d *dashboard) changeGroupState(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid request", "Reload the page and try again.")
		return
	}
	key, err := investigation.DecodeGroupKey(r.PostForm.Get("group"))
	state := investigation.GroupState(r.PostForm.Get("state"))
	if err != nil || state != investigation.GroupOpen && state != investigation.GroupResolved && state != investigation.GroupMuted {
		d.problem(w, r, http.StatusBadRequest, "Invalid group change", "Open the group from Error groups and try again.")
		return
	}
	target := r.PostForm.Get("return")
	if !strings.HasPrefix(target, "/failures") {
		target = "/failures?view=groups"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	version := ""
	if state == investigation.GroupResolved {
		version = strings.TrimSpace(r.PostForm.Get("version"))
	}
	if len(version) > 256 {
		d.problem(w, r, http.StatusBadRequest, "Invalid version", "Use a version of up to 256 bytes.")
		return
	}
	if err := d.store.SetGroupState(ctx, key, state, version, time.Now()); err != nil {
		d.unavailable(w, r, ctx)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (d *dashboard) testNotification(w http.ResponseWriter, r *http.Request) {
	result := "sent"
	if err := d.settings.Notifications.Test(r.Context()); err != nil {
		log.Printf("notifications: %v", err)
		result = "failed"
	}
	http.Redirect(w, r, "/settings?notification="+result+"#notifications", http.StatusSeeOther)
}
