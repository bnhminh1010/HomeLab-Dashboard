package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/gin-gonic/gin"
)

var widgetBookmarkIcons = map[string]struct{}{
	"link": {}, "server": {}, "storage": {}, "network": {},
}

func validWidgetBookmarkURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

type launchpadRequest struct {
	Items    []model.LaunchpadBookmark `json:"items"`
	Revision int64                     `json:"revision"`
}
type operatorNoteRequest struct {
	Text     string `json:"text"`
	Revision int64  `json:"revision"`
}

func (s *Server) getLaunchpad(c *gin.Context) {
	items, revision, err := s.options.WidgetContent.ListLaunchpadBookmarks(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "widget_content_unavailable", "Unable to load launchpad bookmarks.", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "revision": revision})
}

func (s *Server) putLaunchpad(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var req launchpadRequest
	if !decodeJSON(c, &req) {
		return
	}
	if len(req.Items) > 24 {
		writeError(c, http.StatusUnprocessableEntity, "invalid_widget_content", "At most 24 bookmarks are allowed.", nil)
		return
	}
	seen := map[string]struct{}{}
	for i := range req.Items {
		item := &req.Items[i]
		item.ID = strings.TrimSpace(item.ID)
		item.Title = strings.TrimSpace(item.Title)
		item.URL = strings.TrimSpace(item.URL)
		item.Tag = strings.TrimSpace(item.Tag)
		item.Icon = strings.TrimSpace(item.Icon)
		if item.ID == "" || strings.ContainsAny(item.ID, "\x00\r\n") || item.Title == "" || strings.ContainsAny(item.Title, "\x00\r\n") || utf8.RuneCountInString(item.Title) > 80 || strings.ContainsAny(item.Tag, "\x00\r\n") || utf8.RuneCountInString(item.Tag) > 16 || !validWidgetBookmarkURL(item.URL) {
			writeError(c, http.StatusUnprocessableEntity, "invalid_widget_content", "Bookmark fields are invalid.", nil)
			return
		}
		if _, allowed := widgetBookmarkIcons[item.Icon]; !allowed {
			writeError(c, http.StatusUnprocessableEntity, "invalid_widget_content", "Bookmark icon is not supported.", nil)
			return
		}
		if _, exists := seen[item.ID]; exists {
			writeError(c, http.StatusUnprocessableEntity, "invalid_widget_content", "Bookmark IDs must be unique.", nil)
			return
		}
		seen[item.ID] = struct{}{}
	}
	revision, err := s.options.WidgetContent.ReplaceLaunchpadBookmarks(c.Request.Context(), req.Items, req.Revision, principal.Login)
	if errors.Is(err, store.ErrRevisionConflict) {
		writeError(c, http.StatusConflict, "widget_content_conflict", "Launchpad changed; reload before saving.", nil)
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "widget_content_update_failed", "Unable to save launchpad bookmarks.", nil)
		return
	}
	s.audit(c, principal, "widgets.launchpad.update", "launchpad", "success")
	c.JSON(http.StatusOK, gin.H{"items": req.Items, "revision": revision})
}

func (s *Server) getOperatorNote(c *gin.Context) {
	note, err := s.options.WidgetContent.GetOperatorNote(c.Request.Context())
	if err != nil {
		writeError(c, 500, "widget_content_unavailable", "Unable to load operator note.", nil)
		return
	}
	c.JSON(http.StatusOK, note)
}
func (s *Server) putOperatorNote(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var req operatorNoteRequest
	if !decodeJSON(c, &req) {
		return
	}
	if len(req.Text) > 4096 || strings.ContainsRune(req.Text, '\x00') {
		writeError(c, 422, "invalid_widget_content", "Operator note must not exceed 4096 bytes.", nil)
		return
	}
	note, err := s.options.WidgetContent.UpdateOperatorNote(c.Request.Context(), req.Text, req.Revision, principal.Login)
	if errors.Is(err, store.ErrRevisionConflict) {
		writeError(c, 409, "widget_content_conflict", "Operator note changed; reload before saving.", nil)
		return
	}
	if err != nil {
		writeError(c, 500, "widget_content_update_failed", "Unable to save operator note.", nil)
		return
	}
	s.audit(c, principal, "widgets.operator_note.update", "operator_note", "success")
	c.JSON(http.StatusOK, note)
}
