// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
)

// renderable declares an artifact whose media type a browser would render, with a version full of
// characters a filename must not carry, and stores its content.
func (e *env) renderable(t *testing.T, tenant, token, version string, content []byte) *model.Artifact {
	t.Helper()
	ctx := ctxOf(tenant)
	sum := sha256.Sum256(content)
	mt := "text/html"
	a, err := e.api.CreateArtifact(ctx, &model.ArtifactCreateRequest{
		Token: token, Kind: model.KindFirmware, Name: "page", Version: version, Component: "firmware",
		SizeBytes: int64(len(content)), Digest: model.DigestPrefix + hex.EncodeToString(sum[:]),
		CompatibleProfiles: []string{"gateway"}, MediaType: &mt,
	})
	require.NoError(t, err)
	require.Equal(t, "text/html", a.MediaType, "the declared type is kept as catalogue metadata")
	ref, err := e.store.Put(ctx, blob.Key{Tenant: tenant, Purpose: model.BlobPurpose, ID: token + "-object"},
		bytes.NewReader(content), blob.PutOptions{MaxSize: int64(len(content))})
	require.NoError(t, err)
	stamp, err := e.api.ClaimUpload(ctx, a)
	require.NoError(t, err)
	require.NoError(t, e.api.CompleteUpload(ctx, a, stamp, ref))
	a, err = e.api.ArtifactByToken(ctx, token)
	require.NoError(t, err)
	return a
}

func requireInert(t *testing.T, resp *http.Response) {
	t.Helper()
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	require.Equal(t, "sandbox; default-src 'none'", resp.Header.Get("Content-Security-Policy"))
}

func requireAttachment(t *testing.T, resp *http.Response, filename string) {
	t.Helper()
	requireInert(t, resp)
	require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, `attachment; filename="`+filename+`"`, resp.Header.Get("Content-Disposition"))
	require.Equal(t, "text/html", resp.Header.Get(MediaTypeHeader))
}

// A declared media type is never the Content-Type of a download: a whole, a ranged and a HEAD
// response are all opaque attachments under a policy that runs nothing.
func TestADeclaredMediaTypeIsNeverServedAsTheContentType(t *testing.T) {
	e := newEnv(t, 4)
	data := []byte("<html><body><script>void 0</script></body></html>")
	e.renderable(t, "acme", "page-1", `2.0 "beta"; x=<y>/../é`, data)
	_, minted := e.grant(t, "acme", "dev-1", "page-1")
	target := e.rebase(t, minted)
	const want = "page-1-2.0__beta___x__y__..___"

	resp, body := get(t, target)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, data, body)
	requireAttachment(t, resp, want)

	resp, body = get(t, target, "Range", "bytes=6-")
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	require.Equal(t, data[6:], body)
	requireAttachment(t, resp, want)

	req, err := http.NewRequest(http.MethodHead, target, nil)
	require.NoError(t, err)
	hr, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer hr.Body.Close()
	hb, _ := io.ReadAll(hr.Body)
	require.Equal(t, http.StatusOK, hr.StatusCode)
	require.Empty(t, hb)
	requireAttachment(t, hr, want)
}

// A refusal carries the same policy as a download.
func TestARefusalIsInert(t *testing.T) {
	e := newEnv(t, 4)
	e.renderable(t, "acme", "page-1", "1", []byte("<html></html>"))
	_, minted := e.grant(t, "acme", "dev-1", "page-1")
	target := e.rebase(t, minted)

	resp, _ := get(t, target, "Range", "bytes=999-")
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.StatusCode)
	requireInert(t, resp)
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain"))

	resp, _ = get(t, strings.Replace(target, "cap=", "cap=x", 1))
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	requireInert(t, resp)
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain"))
}

// A device that resumes a download of such an artifact still reassembles it exactly.
func TestADeviceStillResumesARenderableArtifact(t *testing.T) {
	e := newEnv(t, 4)
	data := bytes.Repeat([]byte("<p>chunk</p>"), 4000)
	a := e.renderable(t, "acme", "page-1", "1", data)
	_, minted := e.grant(t, "acme", "dev-1", "page-1")
	target := e.rebase(t, minted)

	first, head := get(t, target, "Range", "bytes=0-9999")
	require.Equal(t, http.StatusPartialContent, first.StatusCode)
	rest, tail := get(t, target, "Range", "bytes=10000-", "If-Range", first.Header.Get("ETag"))
	require.Equal(t, http.StatusPartialContent, rest.StatusCode)
	got := append(append([]byte{}, head...), tail...)
	sum := sha256.Sum256(got)
	require.Equal(t, a.Digest, model.DigestPrefix+hex.EncodeToString(sum[:]))
}

func TestAttachmentNameIsSafeASCII(t *testing.T) {
	cases := []struct{ token, version, want string }{
		{"fw-1", "fw-1", "fw-1"},
		{"fw-1", "", "fw-1"},
		{"fw-1", "1.2.3", "fw-1-1.2.3"},
		{"fw-1", "a\"b\\c", "fw-1-a_b_c"},
		{"fw-1", "x;filename=evil.html", "fw-1-x_filename_evil.html"},
		{"fw-1", "../../etc/passwd", "fw-1-.._.._etc_passwd"},
		{"fw-1", "ünï", "fw-1-__n__"},
		{"", ".hidden", "artifact.hidden"},
		{"", "", "artifact"},
		{strings.Repeat("a", 200), "1", strings.Repeat("a", maxAttachmentName)},
	}
	for _, c := range cases {
		got := attachmentName(c.token, c.version)
		require.Equal(t, c.want, got, "token %q version %q", c.token, c.version)
		require.LessOrEqual(t, len(got), maxAttachmentName)
		for i := 0; i < len(got); i++ {
			ch := got[i]
			ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.'
			require.True(t, ok, "byte %q in %q", ch, got)
		}
	}
}
