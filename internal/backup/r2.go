package backup

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	maxObjectBytes = int64(5<<30 - 5<<20)
	objectStem     = "404-probe-backup-"
	objectSuffix   = ".tar.zst.age"
	archiveHash    = "X-Amz-Meta-404-Probe-Archive-Sha256"
)

var (
	accountPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	bucketPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	accessPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	objectPattern  = regexp.MustCompile(`^404-probe-backup-([0-9]{8}T[0-9]{6}(?:\.[0-9]{9})?Z)-schema[1-9][0-9]*\.tar\.zst\.age$`)
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	emptySHA256    = sha256Hex(nil)
	backupHTTP     = &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       time.Minute,
			MaxIdleConns:          4,
			DisableCompression:    true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

type Destination struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

type Credentials struct {
	AccessKey string `json:"access_key_id"`
	SecretKey string `json:"secret_access_key"`
}

type R2Config struct {
	Destination
	Credentials
}

func (c R2Config) configured() bool { return c.AccessKey != "" && c.SecretKey != "" }

func (c *R2Config) normalize() (*url.URL, error) {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if !bucketPattern.MatchString(c.Bucket) {
		return nil, errors.New("invalid R2 bucket name")
	}
	if len(c.Prefix) > 900 || strings.ContainsAny(c.Prefix, "\\\x00\r\n\t") || hasDotSegment(c.Prefix) {
		return nil, errors.New("invalid R2 object prefix")
	}
	if !accessPattern.MatchString(c.AccessKey) || len(c.SecretKey) > 256 || c.SecretKey == "" || strings.TrimSpace(c.SecretKey) != c.SecretKey {
		return nil, errors.New("invalid or missing R2 credentials")
	}
	endpoint := c.Endpoint
	if accountPattern.MatchString(endpoint) {
		endpoint = "https://" + strings.ToLower(endpoint) + ".r2.cloudflarestorage.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(endpoint, "#") || hasDotSegment(u.Path) {
		return nil, errors.New("enter an R2 account ID or a valid endpoint URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("R2 endpoint requires HTTPS (HTTP is allowed on literal loopback addresses)")
	}
	return u, nil
}

func hasDotSegment(path string) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

type r2Client struct {
	config   R2Config
	endpoint url.URL
}

func newR2(c R2Config) (*r2Client, error) {
	u, err := c.normalize()
	if err != nil {
		return nil, err
	}
	return &r2Client{config: c, endpoint: *u}, nil
}

func (c *r2Client) prefix() string {
	if c.config.Prefix == "" {
		return ""
	}
	return c.config.Prefix + "/"
}

func (c *r2Client) request(ctx context.Context, method, key, query string, headers http.Header, body io.Reader, size int64, digest string) (*http.Response, error) {
	u := c.endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/" + c.config.Bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath = uriEncode(u.Path, true)
	u.RawQuery = query
	r, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("invalid R2 request")
	}
	if headers != nil {
		r.Header = headers
	}
	r.ContentLength = size
	signR2(r, c.config.Credentials, time.Now(), digest, "auto")
	response, err := backupHTTP.Do(r)
	if err != nil {
		// Transport errors may include a URL or attacker-controlled text.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errR2Network
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	response.Body.Close()
	return nil, r2Error(response.StatusCode)
}

type r2Error int

func (e r2Error) Error() string {
	switch e {
	case 401, 403:
		return "R2 rejected the credentials or permissions"
	case 404:
		return "R2 bucket or object does not exist"
	case 412:
		return "R2 object already exists or has changed"
	default:
		return fmt.Sprintf("R2 returned HTTP %d", e)
	}
}

func signR2(r *http.Request, credentials Credentials, now time.Time, digest, region string) {
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	r.Header.Set("X-Amz-Date", stamp)
	r.Header.Set("X-Amz-Content-Sha256", digest)
	names := []string{"host"}
	for header := range r.Header {
		names = append(names, strings.ToLower(header))
	}
	slices.Sort(names)
	var canonical strings.Builder
	for _, name := range names {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.URL.Host
		}
		fmt.Fprintf(&canonical, "%s:%s\n", name, strings.Join(strings.Fields(value), " "))
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, canonical.String(), signed, digest}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + sha256Hex([]byte(request))
	key := mac(mac(mac(mac([]byte("AWS4"+credentials.SecretKey), day), region), "s3"), "aws4_request")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+credentials.AccessKey+"/"+scope+",SignedHeaders="+signed+",Signature="+hex.EncodeToString(mac(key, toSign)))
}

func mac(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func uriEncode(s string, slash bool) string {
	const hexChars = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("-_.~", b) >= 0 || slash && b == '/' {
			out.WriteByte(b)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hexChars[b>>4])
		out.WriteByte(hexChars[b&15])
	}
	return out.String()
}

func (c *r2Client) put(ctx context.Context, key string, body io.Reader, size int64, digest string, archive bool) error {
	headers := make(http.Header)
	headers.Set("If-None-Match", "*")
	if archive {
		headers.Set(archiveHash, digest)
	}
	r, err := c.request(ctx, http.MethodPut, key, "", headers, body, size, digest)
	if errors.Is(err, r2Error(412)) && archive {
		// A completed PUT whose response was lost is safe to retry.
		head, headErr := c.head(ctx, key)
		if headErr == nil && head.Get(archiveHash) == digest {
			return nil
		}
	}
	if err == nil {
		r.Body.Close()
	}
	return err
}

func (c *r2Client) head(ctx context.Context, key string) (http.Header, error) {
	r, err := c.request(ctx, http.MethodHead, key, "", nil, nil, 0, emptySHA256)
	if err != nil {
		return nil, err
	}
	r.Body.Close()
	return r.Header, nil
}

func (c *r2Client) delete(ctx context.Context, key, etag string) error {
	headers := make(http.Header)
	if etag != "" {
		headers.Set("If-Match", etag)
	}
	r, err := c.request(ctx, http.MethodDelete, key, "", headers, nil, 0, emptySHA256)
	if err == nil {
		r.Body.Close()
	}
	return err
}

func (c *r2Client) test(ctx context.Context) error {
	key := c.prefix() + "404-probe-connection-test-" + hex.EncodeToString(randBytes())
	const body = "404-probe R2 connection test\n"
	err := c.put(ctx, key, strings.NewReader(body), int64(len(body)), sha256Hex([]byte(body)), false)
	if err != nil {
		// A failed response can arrive after R2 wrote the object. The unique
		// test key permits a best-effort cleanup without repeating the PUT.
		var status r2Error
		if errors.As(err, &status) && status < 500 {
			return err
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = c.delete(cleanup, key, "")
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.delete(cleanup, key, ""); err != nil {
		return fmt.Errorf("remove R2 connection test object: %w", err)
	}
	return nil
}

func randBytes() []byte {
	b := make([]byte, 16)
	rand.Read(b)
	return b
}

type listedObject struct {
	Key  string
	ETag string
	at   time.Time
}

func (c *r2Client) list(ctx context.Context) ([]listedObject, error) {
	var objects []listedObject
	var token string
	for range 100 {
		query := "list-type=2&prefix=" + uriEncode(c.prefix()+objectStem, false)
		if token != "" {
			query = "continuation-token=" + uriEncode(token, false) + "&" + query
		}
		r, err := c.request(ctx, http.MethodGet, "", query, nil, nil, 0, emptySHA256)
		if err != nil {
			return nil, err
		}
		var page struct {
			XMLName   xml.Name `xml:"ListBucketResult"`
			Contents  []listedObject
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&page)
		r.Body.Close()
		if err != nil {
			return nil, errors.New("invalid R2 object listing")
		}
		for _, object := range page.Contents {
			name, ok := strings.CutPrefix(object.Key, c.prefix())
			if !ok {
				continue
			}
			parts := objectPattern.FindStringSubmatch(name)
			if parts == nil {
				continue
			}
			object.at, err = time.Parse("20060102T150405Z", parts[1])
			if err == nil {
				objects = append(objects, object)
			}
		}
		if !page.Truncated {
			return objects, nil
		}
		if page.Next == "" || page.Next == token || len(page.Next) > 4096 {
			break
		}
		token = page.Next
	}
	return nil, errors.New("R2 object listing exceeded its pagination limit")
}

func (c *r2Client) listOwned(ctx context.Context) ([]listedObject, error) {
	objects, err := c.list(ctx)
	if err != nil {
		return nil, err
	}
	owned := objects[:0]
	for _, object := range objects {
		head, err := c.head(ctx, object.Key)
		if errors.Is(err, r2Error(404)) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Leave user objects alone even if their names match the format.
		if hashPattern.MatchString(head.Get(archiveHash)) && object.ETag != "" && head.Get("ETag") == object.ETag {
			owned = append(owned, object)
		}
	}
	return owned, nil
}

func (c *r2Client) prune(ctx context.Context, keep int, uploaded string) (int, error) {
	objects, err := c.listOwned(ctx)
	if err != nil {
		return 0, err
	}
	slices.SortFunc(objects, func(a, b listedObject) int { return b.at.Compare(a.at) })
	count := len(objects)
	for i := len(objects) - 1; i >= 0 && count > keep; i-- {
		object := objects[i]
		if object.Key == uploaded {
			continue
		}
		if err := c.delete(ctx, object.Key, object.ETag); err != nil {
			return count, err
		}
		count--
	}
	return count, nil
}

// Download opens the backup object as a stream. The caller owns the body.
func Download(ctx context.Context, config R2Config, key string) (io.ReadCloser, error) {
	if key == "" || len(key) > 1024 || strings.ContainsAny(key, "\x00\r\n") || hasDotSegment(key) {
		return nil, errors.New("invalid R2 object name")
	}
	c, err := newR2(config)
	if err != nil {
		return nil, err
	}
	r, err := c.request(ctx, http.MethodGet, key, "", nil, nil, 0, emptySHA256)
	if err != nil {
		return nil, err
	}
	if r.ContentLength > maxObjectBytes {
		r.Body.Close()
		return nil, errors.New("R2 object exceeds the backup format limit")
	}
	return r.Body, nil
}
