package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

const webPasswordAttemptLimit = 5

func (w *webAuthenticator) currentCredential(ctx context.Context) (storage.WebCredential, error) {
	credential, err := w.store.WebCredential(ctx)
	if err != nil {
		return storage.WebCredential{}, err
	}
	if credential.Revision == 0 {
		credential.PasswordHash = w.passwordHash
		credential.FileSHA256 = w.passwordFileSHA256
	}
	return credential, nil
}

var errWebPasswordUnicode = errors.New("invalid password Unicode")

// encoding/json replaces invalid UTF-8 and unpaired UTF-16 surrogates by
// default. A password must round-trip without those replacements.
type webPasswordJSON string

func (p *webPasswordJSON) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errWebPasswordUnicode
	}
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	for i := 1; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		unit, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			if i+6 >= len(data) || string(data[i+1:i+3]) != `\u` {
				return errWebPasswordUnicode
			}
			low, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return errWebPasswordUnicode
			}
			i += 6
		case unit >= 0xdc00 && unit <= 0xdfff:
			return errWebPasswordUnicode
		}
	}
	*p = webPasswordJSON(decoded)
	return nil
}

type webPasswordResponseError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Remaining  int    `json:"remaining,omitempty"`
	Characters *int   `json:"characters,omitempty"`
}

type webPasswordErrorEnvelope struct {
	Error webPasswordResponseError `json:"error"`
}

func (a *App) handleChangeWebPassword(w http.ResponseWriter, r *http.Request) {
	const generic = "could not change password"
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", generic)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", generic)
		return
	}
	var request struct {
		Current *webPasswordJSON `json:"current_password"`
		New     *webPasswordJSON `json:"new_password"`
		Confirm *webPasswordJSON `json:"confirm_password"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); errors.Is(err, errWebPasswordUnicode) {
		writeJobError(w, http.StatusBadRequest, "invalid_chars", generic)
		return
	} else if err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || request.Current == nil || request.New == nil || request.Confirm == nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", generic)
		return
	}
	wa := a.webAuth
	session := webSessionFromContext(r.Context())
	if !wa.beginPasswordCheck() {
		w.Header().Set("Retry-After", "1")
		writeJobError(w, http.StatusTooManyRequests, "busy", generic)
		return
	}
	defer wa.endPasswordCheck()
	credential, err := wa.currentCredential(r.Context())
	if err != nil || credential.Revision != session.credRev || auth.ParsePasswordHash(credential.PasswordHash) != nil {
		writeJobError(w, http.StatusUnauthorized, "unauthorized", generic)
		return
	}
	current := []byte(*request.Current)
	if !auth.VerifyPassword(credential.PasswordHash, current) {
		remaining := wa.recordPasswordFailure(session)
		if remaining == 0 {
			wa.clearSessionCookie(w)
			writeJobError(w, http.StatusUnauthorized, "locked_out", generic)
			return
		}
		writeJSON(w, http.StatusBadRequest, webPasswordErrorEnvelope{Error: webPasswordResponseError{Code: "wrong_current", Message: generic, Remaining: remaining}})
		return
	}
	newPassword := []byte(*request.New)
	if problem := auth.ValidateNewWebPassword(newPassword, []byte(*request.Confirm), current); problem != nil {
		response := webPasswordResponseError{Code: problem.Code, Message: generic}
		if problem.Code == "too_short" {
			response.Characters = &problem.Characters
		}
		writeJSON(w, http.StatusBadRequest, webPasswordErrorEnvelope{Error: response})
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", generic)
		return
	}
	now := a.now()
	plain, replacement, err := newWebSession(now, session.origin, session.policyRev, 0)
	if err == nil {
		err = wa.replacePassword(r.Context(), session, replacement, hash, credential.FileSHA256, now)
	}
	if errors.Is(err, storage.ErrWebCredentialConflict) {
		writeJobError(w, http.StatusConflict, "credential_changed", generic)
		return
	}
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", generic)
		return
	}
	wa.setSessionCookie(w, plain, now)
	w.WriteHeader(http.StatusNoContent)
}

func (w *webAuthenticator) recordPasswordFailure(session *webSession) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sessions[session.key] != session {
		return 0
	}
	session.passwordFailures++
	if session.passwordFailures >= webPasswordAttemptLimit {
		w.removeSessionLocked(session)
	}
	return webPasswordAttemptLimit - session.passwordFailures
}

func (w *webAuthenticator) replacePassword(ctx context.Context, current, replacement *webSession, hash, digest string, now time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sessions[current.key] != current || !now.Before(sessionDeadline(current)) {
		return storage.ErrWebCredentialConflict
	}
	revision, err := w.store.ReplaceWebCredential(ctx, current.credRev, hash, digest, now)
	if err != nil {
		return err
	}
	for _, session := range w.sessions {
		w.removeSessionLocked(session)
	}
	replacement.credRev = revision
	w.sessions[replacement.key] = replacement
	return nil
}
