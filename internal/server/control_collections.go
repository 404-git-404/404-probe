package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"404-probe/internal/storage"
)

const (
	defaultControlCollectionLimit = 50
	maxControlCursorBytes         = 2048
	controlCursorVersion          = 1
)

var (
	errInvalidControlQuery  = errors.New("invalid control query")
	errInvalidControlCursor = errors.New("invalid control cursor")
)

type controlCollectionCursor struct {
	Version   int    `json:"v"`
	Resource  string `json:"resource"`
	Filters   string `json:"filters"`
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

func parseControlQuery(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errInvalidControlQuery
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name, entries := range values {
		if _, ok := allowedSet[name]; !ok || len(entries) != 1 || entries[0] == "" {
			return nil, errInvalidControlQuery
		}
	}
	return values, nil
}

func parseControlCollectionPage(values url.Values, resource, filters string, validID func(string) bool) (int, *storage.CollectionPageKey, error) {
	limit := defaultControlCollectionLimit
	if value := values.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > storage.MaxCollectionPageLimit {
			return 0, nil, errInvalidControlQuery
		}
		limit = parsed
	}
	if value := values.Get("cursor"); value != "" {
		key, err := decodeControlCursor(value, resource, filters, validID)
		if err != nil {
			return 0, nil, errInvalidControlCursor
		}
		return limit, key, nil
	}
	return limit, nil, nil
}

func encodeControlCursor(resource, filters string, key *storage.CollectionPageKey) (*string, error) {
	if key == nil {
		return nil, nil
	}
	payload, err := json.Marshal(controlCollectionCursor{
		Version: controlCursorVersion, Resource: resource, Filters: filters, CreatedAt: key.CreatedAt, ID: key.ID,
	})
	if err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return &encoded, nil
}

func decodeControlCursor(value, resource, filters string, validID func(string) bool) (*storage.CollectionPageKey, error) {
	if len(value) > maxControlCursorBytes {
		return nil, errInvalidControlCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) == 0 || len(payload) > maxControlCursorBytes {
		return nil, errInvalidControlCursor
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor controlCollectionCursor
	if err := decoder.Decode(&cursor); err != nil {
		return nil, errInvalidControlCursor
	}
	if err := ensureJSONEOF(decoder); err != nil || cursor.Version != controlCursorVersion ||
		cursor.Resource != resource || cursor.Filters != filters || cursor.CreatedAt <= 0 || !validID(cursor.ID) {
		return nil, errInvalidControlCursor
	}
	return &storage.CollectionPageKey{CreatedAt: cursor.CreatedAt, ID: cursor.ID}, nil
}

func controlFilterFingerprint(values url.Values, names ...string) string {
	filters := make(url.Values)
	for _, name := range names {
		if value := values.Get(name); value != "" {
			filters.Set(name, value)
		}
	}
	return filters.Encode()
}

func setControlNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func writeControlCollectionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidControlCursor) {
		writeJobError(w, http.StatusBadRequest, "invalid_cursor", "invalid collection cursor")
		return
	}
	writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid collection query")
}
