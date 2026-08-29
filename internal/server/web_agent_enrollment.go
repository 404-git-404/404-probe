package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"404-probe/internal/auth"
)

const (
	maxWebAgentCreateBodyBytes = 2 << 10
	maxWebAgentNameBytes       = 128
	webAgentInstallerURL       = "https://raw.githubusercontent.com/404-git-404/404-probe/v0.5.0/install.sh"
)

type webAgentCreateRequest struct {
	Name string `json:"name"`
}

type webAgentEnrollmentView struct {
	Agent           webAgentSummaryView `json:"agent"`
	InstallCommand  string              `json:"install_command"`
	EnrollmentValue string              `json:"enrollment_value"`
}

func (a *App) handleCreateWebAgent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid create agent query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentCreateBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	request, err := decodeWebAgentCreateRequest(body)
	if err != nil || !validWebAgentName(request.Name) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent name")
		return
	}

	agentID, err := auth.NewID()
	if err != nil {
		a.logger.Error("create Web Agent ID", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create Agent")
		return
	}
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		a.logger.Error("create Web Agent credential", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create Agent")
		return
	}
	enrollment, err := auth.EncodeEnrollment(agentID, token)
	if err != nil {
		a.logger.Error("encode Web Agent enrollment", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create Agent")
		return
	}
	createdAt := a.now()
	if err := a.store.AddAgent(r.Context(), agentID, request.Name, tokenHash, createdAt); err != nil {
		a.logger.Error("store Web Agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create Agent")
		return
	}

	writeJSON(w, http.StatusCreated, webAgentEnrollmentView{
		Agent: webAgentSummaryView{
			AgentID: agentID, Name: request.Name, CreatedAt: createdAt.UnixMilli(),
		},
		InstallCommand:  webAgentInstallCommand(a.webAuth.publicOrigin.String()),
		EnrollmentValue: enrollment,
	})
}

func decodeWebAgentCreateRequest(body []byte) (webAgentCreateRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request webAgentCreateRequest
	if err := decoder.Decode(&request); err != nil {
		return webAgentCreateRequest{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return webAgentCreateRequest{}, err
	}
	return request, nil
}

func validWebAgentName(value string) bool {
	if value == "" || len(value) > maxWebAgentNameBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func webAgentInstallCommand(origin string) string {
	return fmt.Sprintf("curl -fsSL %s | sudo bash -s -- agent --server %s", shellSingleQuote(webAgentInstallerURL), shellSingleQuote(origin))
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
