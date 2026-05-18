package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
)

func (s *Server) handleListInviteCodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.authEngine.Store().ListInviteCodes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if list == nil {
		list = []*auth.InviteCode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invite_codes": list})
}

func (s *Server) handleCreateInviteCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role      string `json:"role"`
		MaxUses   int    `json:"max_uses"`
		ExpiresAt string `json:"expires_at"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if body.ExpiresAt == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 必填", Code: errs.CodeBadRequest})
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 格式无效，请使用 RFC3339", Code: errs.CodeBadRequest})
		return
	}
	if !expiresAt.After(time.Now()) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 须晚于当前时间", Code: errs.CodeBadRequest})
		return
	}
	role := body.Role
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "role 只能是 user 或 admin", Code: errs.CodeBadRequest})
		return
	}
	maxUses := body.MaxUses
	if maxUses < 1 {
		maxUses = 1
	}
	plain, err := auth.GenerateInvitePlaintext()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	createdBy := ""
	if u := auth.UserFrom(r.Context()); u != nil {
		createdBy = u.ID
	}
	invite := &auth.InviteCode{
		ID:        auth.NewRandomID(),
		CodeHint:  auth.InviteCodeHint(plain),
		Role:      role,
		MaxUses:   maxUses,
		ExpiresAt: expiresAt,
		Note:      body.Note,
		CreatedBy: createdBy,
	}
	if err := s.authEngine.Store().CreateInviteCode(r.Context(), invite, auth.InviteLookupKey(plain), hash); err != nil {
		s.logger.Error("创建邀请码失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	created, _ := s.authEngine.Store().GetInviteCodeByID(r.Context(), invite.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite": created,
		"code":   plain,
	})
}

func (s *Server) handleUpdateInviteCode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Disabled  *bool   `json:"disabled,omitempty"`
		Note      *string `json:"note,omitempty"`
		ExpiresAt *string `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 格式无效", Code: errs.CodeBadRequest})
			return
		}
		if !t.After(time.Now()) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "expires_at 须晚于当前时间", Code: errs.CodeBadRequest})
			return
		}
		expiresAt = &t
	}
	if err := s.authEngine.Store().UpdateInviteCode(r.Context(), id, body.Disabled, body.Note, expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "邀请码不存在", Code: errs.CodeNotFound})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	invite, _ := s.authEngine.Store().GetInviteCodeByID(r.Context(), id)
	writeJSON(w, http.StatusOK, invite)
}

func (s *Server) handleDeleteInviteCode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.authEngine.Store().DeleteInviteCode(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "邀请码不存在", Code: errs.CodeNotFound})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
