package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"404-probe/internal/auth"
	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
)

const (
	maxWebEnrollmentBodyBytes  = 2 << 10
	maxAgentEnrollRequestBytes = 2 << 10
	webOneClickReleaseBaseURL  = "https://github.com/404-git-404/404-probe/releases/download"
	oneClickUnavailableMessage = "此 Server 是开发构建或无法校验的构建，没有对应的正式 release，无法使用一键安装；请使用手动安装。"
)

var enrollmentTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type webAgentEnrollmentTokenView struct {
	EnrollmentID string `json:"enrollment_id"`
	Name         string `json:"name"`
	Command      string `json:"command"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

// handleCreateWebAgentEnrollment issues a single-use, 30-minute registration token bound to the
// Agent name and returns the one-click install command. The token is shown only in this response.
func (a *App) handleCreateWebAgentEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid enrollment query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebEnrollmentBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	request, err := decodeWebAgentCreateRequest(body)
	if err != nil || !validWebAgentName(request.Name) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent name")
		return
	}
	target := webTargetFromContext(r)
	if target == nil {
		writeJobError(w, http.StatusForbidden, "forbidden", "Web request host is not allowed")
		return
	}
	if !webOneClickAvailable(a.buildInfo) {
		writeJobError(w, http.StatusConflict, "install_unavailable", oneClickUnavailableMessage)
		return
	}
	id, err := auth.NewID()
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create enrollment")
		return
	}
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create enrollment")
		return
	}
	now := a.now()
	expiresAt, err := a.store.CreateAgentEnrollmentToken(r.Context(), id, request.Name, tokenHash, now)
	if err != nil {
		if errors.Is(err, storage.ErrEnrollmentLimit) {
			writeJobError(w, http.StatusTooManyRequests, "enrollment_limit", "too many pending enrollment commands; revoke or wait for one to expire")
			return
		}
		a.logger.Error("store enrollment token", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create enrollment")
		return
	}
	command := webOneClickInstallCommand(target.origin.String(), request.Name, token, a.buildInfo)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, webAgentEnrollmentTokenView{
		EnrollmentID: id, Name: request.Name, Command: command, CreatedAt: now.UnixMilli(), ExpiresAt: expiresAt.UnixMilli(),
	})
}

// handleRevokeWebAgentEnrollment voids a token that has not been used yet.
func (a *App) handleRevokeWebAgentEnrollment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("enrollment_id")
	if r.URL.RawQuery != "" || !validEnrollmentID(id) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid enrollment ID")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebEnrollmentBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if err := decodeWebAgentRevokeRequest(body); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid revoke request")
		return
	}
	switch err := a.store.RevokeAgentEnrollmentToken(r.Context(), id, a.now()); {
	case err == nil:
		writeJSON(w, http.StatusOK, struct {
			Revoked bool `json:"revoked"`
		}{true})
	case errors.Is(err, storage.ErrEnrollmentInvalid):
		writeJobError(w, http.StatusNotFound, "enrollment_not_found", "enrollment not found")
	case errors.Is(err, storage.ErrEnrollmentUsed):
		writeJobError(w, http.StatusConflict, "enrollment_used", "enrollment was already used")
	default:
		a.logger.Error("revoke enrollment token", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not revoke enrollment")
	}
}

func validEnrollmentID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type agentEnrollRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

type agentEnrollResponse struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
	Name       string `json:"name"`
}

// handleAgentEnroll lets the installer exchange a registration token for the new Agent's identity
// and credential. It is the only unauthenticated Agent route, so it accepts one tiny JSON object
// and reveals nothing about why a guess failed beyond the token's own state.
func (a *App) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid enrollment query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxAgentEnrollRequestBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request agentEnrollRequest
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil ||
		!enrollmentTokenPattern.MatchString(request.Token) || !validWebAgentName(request.Name) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid enrollment request")
		return
	}
	agentID, err := auth.NewID()
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not enroll Agent")
		return
	}
	agentToken, agentHash, err := auth.NewToken()
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not enroll Agent")
		return
	}
	err = a.store.RedeemAgentEnrollmentToken(r.Context(), auth.Hash(request.Token), request.Name, agentID, agentHash, a.now())
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, agentEnrollResponse{AgentID: agentID, AgentToken: agentToken, Name: request.Name})
	case errors.Is(err, storage.ErrEnrollmentInvalid):
		writeJobError(w, http.StatusUnauthorized, "enrollment_invalid", "the registration token is not valid")
	case errors.Is(err, storage.ErrEnrollmentUsed):
		writeJobError(w, http.StatusConflict, "enrollment_used", "the registration token was already used")
	case errors.Is(err, storage.ErrEnrollmentExpired):
		writeJobError(w, http.StatusGone, "enrollment_expired", "the registration token has expired; generate a new command in the panel")
	case errors.Is(err, storage.ErrEnrollmentRevoked):
		writeJobError(w, http.StatusGone, "enrollment_revoked", "the registration token was revoked")
	case errors.Is(err, storage.ErrEnrollmentNameMismatch):
		writeJobError(w, http.StatusConflict, "enrollment_name_mismatch", "the Agent name does not match the registration token")
	default:
		a.logger.Error("redeem enrollment token", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not enroll Agent")
	}
}

func webOneClickAvailable(info buildinfo.Info) bool {
	return info.UpgradeEligible() && buildinfo.IsCanonicalVersion(info.Version)
}

// webOneClickInstallCommand builds the command copied from the panel. The Server's own release
// installer is fetched by exact version; every value that is not plainly safe is single-quoted.
func webOneClickInstallCommand(origin, name, token string, info buildinfo.Info) string {
	return fmt.Sprintf("curl -fsSL %s/%s/install.sh | sudo env PROBE_404_ROLE=agent PROBE_404_SERVER=%s PROBE_404_ENROLL_TOKEN=%s PROBE_404_AGENT_NAME=%s bash",
		webOneClickReleaseBaseURL, info.Version, shellWord(origin), shellWord(token), shellWord(name))
}

var shellSafeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellWord(value string) string {
	if shellSafeWord.MatchString(value) {
		return value
	}
	return shellSingleQuote(value)
}
