package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/bundle"
)

// TestDevFileSharingPresigned is the presigned-URL contract end to end on
// the file-sharing reference: the application authorizes a request and
// signs URLs, and this test replays exactly what a browser then does
// against the bucket endpoint (the CORS preflight, the signed PUT with
// its declared Content-Type, the exposed ETag, the signed GET, a range
// read, multipart parts) plus every way a URL must fail (tampered
// signature, other method, other key, other host, expired, unsigned,
// other bucket, anonymous). The dev store has no public domain, so the
// application signs for the loopback port the local platform maps the
// gateway to; the HTTPS-edge variant lives in the cluster suite.
func TestDevFileSharingPresigned(t *testing.T) {
	h := newE2EHarnessFor(t, "file-sharing", "file-sharing.localhost")
	const token = "e2e-upload-token"
	s3Public := fmt.Sprintf("http://127.0.0.1:%d", e2eLoopbackBase+bundle.S3NodePort-bundle.PoolNodePortMin)
	envFile := filepath.Join(h.projectDir, ".env")
	current, err := os.ReadFile(envFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(envFile, append(current,
		[]byte("UPLOAD_TOKEN="+token+"\nS3_PUBLIC_ENDPOINT="+s3Public+"\n")...), 0o644))
	origin := fmt.Sprintf("https://%s:%d", h.host, e2eHTTPSPort)

	out := h.run(false, "", "dev", "-d", "--skalid-image", "skalid:dev")
	require.Contains(t, out, "ready")
	// The release command (migrate up) ran before the rollout; the page
	// answers once the database and the bucket are both injected.
	h.waitRoute("File sharing", 10*time.Minute)

	app := &appClient{h: h, token: token}
	browser := &http.Client{Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Signing needs the request authorized first.
	status, _ := app.call(t, http.MethodPost, "/api/uploads", "", map[string]any{"name": "x", "size": 1})
	require.Equal(t, http.StatusUnauthorized, status)

	// A key that needs URL encoding: spaces, brackets, non-ASCII.
	const name = "report 2026 (final) ü.txt"
	content := []byte("bytes that never pass through the application\n")
	contentType := "text/plain; charset=utf-8"
	upload := app.createUpload(t, map[string]any{"name": name, "contentType": contentType, "size": len(content)})
	require.True(t, strings.HasPrefix(upload.URL, s3Public+"/"), "signed for the public endpoint: %s", upload.URL)
	require.Equal(t, contentType, upload.Headers["Content-Type"])
	bucket := strings.SplitN(strings.TrimPrefix(mustParse(t, upload.URL).Path, "/"), "/", 2)[0]
	require.Regexp(t, `^b-files-[0-9a-f]{8}$`, bucket)

	// The browser's preflight: the fallback CORS policy of the pinned
	// engine admits any origin, PUT among the methods, the declared
	// request headers, and exposes ETag on the actual response.
	response, _ := send(t, browser, http.MethodOptions, upload.URL, http.Header{
		"Origin":                         {origin},
		"Access-Control-Request-Method":  {http.MethodPut},
		"Access-Control-Request-Headers": {"content-type"},
	}, nil)
	require.Contains(t, []int{http.StatusOK, http.StatusNoContent}, response.StatusCode, "preflight")
	require.Contains(t, []string{origin, "*"}, response.Header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, response.Header.Get("Access-Control-Allow-Methods"), http.MethodPut)
	allowHeaders := strings.ToLower(response.Header.Get("Access-Control-Allow-Headers"))
	require.True(t, allowHeaders == "*" || strings.Contains(allowHeaders, "content-type"),
		"preflight must admit the declared header: %q", allowHeaders)

	// The signed PUT carries exactly the declared Content-Type (it is part
	// of the signature) and the browser's Origin.
	response, body := send(t, browser, http.MethodPut, upload.URL, http.Header{
		"Origin": {origin}, "Content-Type": {contentType},
	}, content)
	require.Equal(t, http.StatusOK, response.StatusCode, "signed upload: %s", body)
	etag := response.Header.Get("ETag")
	require.NotEmpty(t, etag, "the bucket must answer with an ETag")
	require.Contains(t, []string{origin, "*"}, response.Header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, strings.ToLower(response.Header.Get("Access-Control-Expose-Headers")), "etag")

	// Publishing verifies the object landed with the declared size.
	status, raw := app.call(t, http.MethodPost, "/api/uploads/"+upload.ID+"/complete", token, nil)
	require.Equal(t, http.StatusOK, status, "complete: %s", raw)
	var record fileRecordJSON
	require.NoError(t, json.Unmarshal(raw, &record))
	require.Equal(t, "complete", record.Status)
	require.EqualValues(t, len(content), record.Size)

	// A browser that sends fewer bytes than it declared cannot publish:
	// the server's HEAD catches it and discards the object.
	short := app.createUpload(t, map[string]any{"name": "short.bin", "size": len(content) + 1})
	response, body = send(t, browser, http.MethodPut, short.URL, http.Header{"Content-Type": {"application/octet-stream"}}, content)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
	status, raw = app.call(t, http.MethodPost, "/api/uploads/"+short.ID+"/complete", token, nil)
	require.Equal(t, http.StatusConflict, status, "%s", raw)

	// Listing and the signed download: the response-content-disposition
	// is part of the signed query, so the browser saves the original name.
	status, raw = app.call(t, http.MethodGet, "/api/files", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(raw), record.ID)
	require.NotContains(t, string(raw), short.ID, "an unpublished upload is never listed")
	status, raw = app.call(t, http.MethodGet, "/api/files/"+record.ID, "", nil)
	require.Equal(t, http.StatusOK, status)
	var download struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.Unmarshal(raw, &download))
	require.True(t, strings.HasPrefix(download.URL, s3Public+"/"), download.URL)
	response, body = send(t, browser, http.MethodGet, download.URL, http.Header{"Origin": {origin}}, nil)
	require.Equal(t, http.StatusOK, response.StatusCode, "signed download: %s", body)
	require.Equal(t, content, body)
	require.Equal(t, contentType, response.Header.Get("Content-Type"))
	require.Contains(t, response.Header.Get("Content-Disposition"), "attachment")
	require.Contains(t, []string{origin, "*"}, response.Header.Get("Access-Control-Allow-Origin"))

	// The share link redirects to a fresh signed GET.
	response, _ = h.requestFull(t, http.MethodGet, "/files/"+record.ID, nil, nil)
	require.Equal(t, http.StatusFound, response.StatusCode)
	require.True(t, strings.HasPrefix(response.Header.Get("Location"), s3Public+"/"), response.Header.Get("Location"))

	// Range reads work on a signed URL (video seeking, resumable downloads).
	response, body = send(t, browser, http.MethodGet, download.URL, http.Header{"Range": {"bytes=0-4"}}, nil)
	require.Equal(t, http.StatusPartialContent, response.StatusCode)
	require.Equal(t, content[:5], body)
	require.Contains(t, response.Header.Get("Content-Range"), fmt.Sprintf("bytes 0-4/%d", len(content)))

	// Every way a signed URL must fail. Each attempt reuses a fresh signed
	// PUT so the object itself never appears.
	deny := func(t *testing.T, what string, response *http.Response, body []byte) {
		t.Helper()
		require.Equal(t, http.StatusForbidden, response.StatusCode, "%s must be refused: %d %s", what, response.StatusCode, body)
	}
	fresh := app.createUpload(t, map[string]any{"name": "never.bin", "size": len(content)})
	signed := mustParse(t, fresh.URL)
	tampered := *signed
	query := tampered.Query()
	signature := query.Get("X-Amz-Signature")
	query.Set("X-Amz-Signature", flipHex(signature))
	tampered.RawQuery = query.Encode()
	response, body = send(t, browser, http.MethodPut, tampered.String(), octet(), content)
	deny(t, "a tampered signature", response, body)
	response, body = send(t, browser, http.MethodDelete, fresh.URL, nil, nil)
	deny(t, "another method on a PUT URL", response, body)
	otherKey := *signed
	otherKey.RawPath, otherKey.Path = "", signed.Path+"-other"
	response, body = send(t, browser, http.MethodPut, otherKey.String(), octet(), content)
	deny(t, "another key under the same signature", response, body)
	request, err := http.NewRequest(http.MethodPut, fresh.URL, bytes.NewReader(content))
	require.NoError(t, err)
	request.Host = "files.example.com"
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = browser.Do(request)
	require.NoError(t, err)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	deny(t, "another Host than the one signed", response, body)
	unsigned := *signed
	unsigned.RawQuery = ""
	response, body = send(t, browser, http.MethodPut, unsigned.String(), octet(), content)
	deny(t, "an unsigned request", response, body)
	otherBucket := *signed
	otherBucket.RawPath, otherBucket.Path = "", strings.Replace(signed.Path, "/"+bucket+"/", "/b-other-00000000/", 1)
	response, body = send(t, browser, http.MethodPut, otherBucket.String(), octet(), content)
	deny(t, "another bucket", response, body)
	response, body = send(t, browser, http.MethodGet, s3Public+"/"+bucket+"?list-type=2", nil, nil)
	deny(t, "an anonymous listing", response, body)
	plain := mustParse(t, download.URL)
	plain.RawQuery = ""
	response, body = send(t, browser, http.MethodGet, plain.String(), nil, nil)
	deny(t, "an anonymous read of a private object", response, body)
	expiring := app.createUpload(t, map[string]any{"name": "late.bin", "size": len(content), "expiresIn": "1s"})
	time.Sleep(3 * time.Second)
	response, body = send(t, browser, http.MethodPut, expiring.URL, octet(), content)
	deny(t, "an expired URL", response, body)

	// Multipart: one signed URL per part, ETags collected by the browser,
	// assembled server-side on completion.
	first := make([]byte, 5<<20)
	_, err = rand.Read(first)
	require.NoError(t, err)
	second := []byte(strings.Repeat("tail\n", 200))
	whole := append(append([]byte{}, first...), second...)
	multipart := app.createUpload(t, map[string]any{"name": "large.bin", "size": len(whole), "parts": 2})
	require.NotEmpty(t, multipart.UploadID)
	require.Len(t, multipart.Parts, 2)
	var parts []map[string]any
	for i, part := range multipart.Parts {
		payload := first
		if i == 1 {
			payload = second
		}
		require.True(t, strings.HasPrefix(part.URL, s3Public+"/"), part.URL)
		response, body = send(t, browser, http.MethodPut, part.URL, http.Header{"Origin": {origin}}, payload)
		require.Equal(t, http.StatusOK, response.StatusCode, "part %d: %s", part.PartNumber, body)
		require.NotEmpty(t, response.Header.Get("ETag"))
		parts = append(parts, map[string]any{"partNumber": part.PartNumber, "etag": response.Header.Get("ETag")})
	}
	status, raw = app.call(t, http.MethodPost, "/api/uploads/"+multipart.ID+"/complete", token, map[string]any{"parts": parts})
	require.Equal(t, http.StatusOK, status, "complete multipart: %s", raw)
	require.NoError(t, json.Unmarshal(raw, &record))
	require.EqualValues(t, len(whole), record.Size)
	status, raw = app.call(t, http.MethodGet, "/api/files/"+record.ID, "", nil)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, json.Unmarshal(raw, &download))
	response, body = send(t, browser, http.MethodGet, download.URL, nil, nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, sha256.Sum256(whole), sha256.Sum256(body), "the assembled object must be byte-identical")

	// Cancelling a multipart upload frees what the bucket held open.
	abandoned := app.createUpload(t, map[string]any{"name": "abandoned.bin", "size": len(whole), "parts": 2})
	response, body = send(t, browser, http.MethodPut, abandoned.Parts[0].URL, nil, first)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
	status, raw = app.call(t, http.MethodGet, "/api/uploads", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(raw), abandoned.UploadID, "the open multipart upload is visible")
	status, raw = app.call(t, http.MethodDelete, "/api/uploads/"+abandoned.ID, token, nil)
	require.Equal(t, http.StatusOK, status, "%s", raw)
	status, raw = app.call(t, http.MethodGet, "/api/uploads", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, string(raw), abandoned.UploadID, "aborting must drop the multipart upload")

	// Deleting a file invalidates every link to it: a valid signature on
	// a missing object is a 404, never a leak of something else.
	status, raw = app.call(t, http.MethodDelete, "/api/files/"+record.ID, token, nil)
	require.Equal(t, http.StatusOK, status, "%s", raw)
	response, _ = send(t, browser, http.MethodGet, download.URL, nil, nil)
	require.Equal(t, http.StatusNotFound, response.StatusCode)

	out = h.run(false, "", "dev", "status")
	require.Contains(t, out, "bucket.files")
	require.Contains(t, out, "healthy")
	h.run(false, "", "dev", "down")
}

type uploadJSON struct {
	ID       string            `json:"id"`
	Key      string            `json:"key"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	UploadID string            `json:"uploadId"`
	Parts    []struct {
		PartNumber int    `json:"partNumber"`
		URL        string `json:"url"`
	} `json:"parts"`
}

type fileRecordJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Size   int64  `json:"size"`
}

// appClient talks to the file-sharing application through the local edge.
type appClient struct {
	h     *e2eHarness
	token string
}

func (c *appClient) call(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	header := http.Header{}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
		header.Set("Content-Type", "application/json")
	}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	response, raw := c.h.requestFull(t, method, path, header, reader)
	return response.StatusCode, raw
}

func (c *appClient) createUpload(t *testing.T, request map[string]any) uploadJSON {
	t.Helper()
	status, raw := c.call(t, http.MethodPost, "/api/uploads", c.token, request)
	require.Equal(t, http.StatusCreated, status, "create upload: %s", raw)
	var upload uploadJSON
	require.NoError(t, json.Unmarshal(raw, &upload))
	return upload
}

// requestFull is request with caller-controlled headers and the whole
// response, for JSON APIs behind the edge.
func (h *e2eHarness) requestFull(t *testing.T, method, path string, header http.Header, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, fmt.Sprintf("https://127.0.0.1:%d%s", e2eHTTPSPort, path), body)
	require.NoError(t, err)
	request.Host = h.host
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := h.httpsClient().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, raw
}

// send performs one request the way a browser would against a bucket
// URL: the URL untouched, only the headers the page sets.
func send(t *testing.T, client *http.Client, method, rawURL string, header http.Header, body []byte) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, rawURL, reader)
	require.NoError(t, err)
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, raw
}

func octet() http.Header {
	return http.Header{"Content-Type": {"application/octet-stream"}}
}

func mustParse(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	return parsed
}

// flipHex changes the first hex digit so the signature stays well-formed
// but wrong.
func flipHex(signature string) string {
	if signature == "" {
		return "0"
	}
	replacement := "0"
	if signature[0] == '0' {
		replacement = "1"
	}
	return replacement + signature[1:]
}
