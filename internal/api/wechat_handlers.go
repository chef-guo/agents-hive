package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
)

func (s *Server) currentWeChatUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	if s.wechatBotService == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "官方 wechatbot 未启用", Code: errs.CodeInternal})
		return nil, false
	}
	if auth.IsAuthEnabled(r.Context()) {
		user := auth.UserFrom(r.Context())
		if user == nil || user.ID == "" {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: errs.CodePermissionDenied})
			return nil, false
		}
		return user, true
	}
	writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "微信连接需要登录用户", Code: errs.CodePermissionDenied})
	return nil, false
}

func (s *Server) handleWeChatStatus(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentWeChatUser(w, r)
	if !ok {
		return
	}
	status, err := s.wechatBotService.Status(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleWeChatLogin(w http.ResponseWriter, r *http.Request) {
	s.handleWeChatLoginWithForce(w, r, false)
}

func (s *Server) handleWeChatRelogin(w http.ResponseWriter, r *http.Request) {
	s.handleWeChatLoginWithForce(w, r, true)
}

func (s *Server) handleWeChatLoginWithForce(w http.ResponseWriter, r *http.Request, force bool) {
	user, ok := s.currentWeChatUser(w, r)
	if !ok {
		return
	}
	status, err := s.wechatBotService.Login(r.Context(), user.ID, force)
	if err != nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleWeChatLogout(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentWeChatUser(w, r)
	if !ok {
		return
	}
	if err := s.wechatBotService.Logout(r.Context(), user.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWeChatConversations(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentWeChatUser(w, r)
	if !ok {
		return
	}
	convs, err := s.wechatBotService.ListConversations(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleWeChatEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentWeChatUser(w, r)
	if !ok {
		return
	}
	events, unsubscribe := s.wechatBotService.Subscribe(user.ID)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "当前连接不支持事件流", Code: errs.CodeInternal})
		return
	}
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\n", ev.Type)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
