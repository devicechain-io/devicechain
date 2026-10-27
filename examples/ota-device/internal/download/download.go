// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package download fetches an image over the capability-gated, range-resumable download endpoint
// (GET .../updates/download/{artifact}?cap=...). The URL carries a bearer secret in its query, so
// nothing here ever prints it: every error is stripped of the URL before it leaves the package.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Errors a caller distinguishes.
var (
	// ErrGrantRejected means the server refused the capability (expired, revoked, or not for this
	// artifact): ask for a new grant.
	ErrGrantRejected = errors.New("download: the grant was refused")
	// ErrSizeMismatch means the server sent more bytes than the assignment declared, or declared a
	// different total. The partial file is not trustworthy and is removed.
	ErrSizeMismatch = errors.New("download: the server's size does not match the assignment")
	// ErrBadRange means the server answered a ranged request with a range other than the one asked.
	ErrBadRange = errors.New("download: the server answered a different range than requested")
	// ErrInterrupted means the body ended early or the connection failed. The partial file is kept
	// and the next call resumes from it.
	ErrInterrupted = errors.New("download: interrupted")
)

// Options configure one fetch.
type Options struct {
	Client *http.Client
	// Size is the number of bytes the assignment declared. The fetch never writes more.
	Size int64
	// Part is the file the bytes accumulate in; its current length is the resume offset.
	Part string
	// IdleTimeout cuts a transfer that delivers nothing for this long, covering the wait for the
	// response headers as well as the body. Zero means 30 seconds.
	IdleTimeout time.Duration
	// IfRange, when set, is sent with a resume as If-Range (the artifact's ETag, quoted): a server
	// whose object has changed answers with the whole of the new one rather than a slice of it.
	IfRange string
	// OnBytes is called with the number of bytes held after each chunk. It may be nil.
	OnBytes func(held int64)
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

// Fetch downloads rawURL into opts.Part, resuming from whatever the file already holds. It
// returns nil when the file holds exactly opts.Size bytes; whether those bytes are the right ones
// is for the digest check, not for this function.
func Fetch(ctx context.Context, rawURL string, opts Options) error {
	if opts.Size < 1 {
		return errors.New("download: size must be positive")
	}
	held := int64(0)
	if fi, err := os.Stat(opts.Part); err == nil {
		held = fi.Size()
	}
	if held > opts.Size {
		// More than the assignment declares can only be a stale file from another image.
		if err := os.Remove(opts.Part); err != nil {
			return err
		}
		held = 0
	}
	if held == opts.Size {
		return nil
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(idle, cancel)
	defer stall.Stop()
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return redact(err)
	}
	if held > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(held, 10)+"-")
		if opts.IfRange != "" {
			req.Header.Set("If-Range", opts.IfRange)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInterrupted, redact(err))
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	switch resp.StatusCode {
	case http.StatusPartialContent:
		m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if m == nil {
			return fmt.Errorf("%w: no usable Content-Range", ErrBadRange)
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		if start != held {
			return fmt.Errorf("%w: asked for offset %d, got %d", ErrBadRange, held, start)
		}
		if m[3] != "*" {
			if total, _ := strconv.ParseInt(m[3], 10, 64); total != opts.Size {
				_ = os.Remove(opts.Part)
				return fmt.Errorf("%w: server total %d, assignment %d", ErrSizeMismatch, total, opts.Size)
			}
		}
	case http.StatusOK:
		// A full body. If a range was asked for, the server ignored it: the body starts at zero,
		// so discard what is held and take it from the top.
		if resp.ContentLength > opts.Size {
			_ = os.Remove(opts.Part)
			return fmt.Errorf("%w: server length %d, assignment %d", ErrSizeMismatch, resp.ContentLength, opts.Size)
		}
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		held = 0
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: HTTP %d", ErrGrantRejected, resp.StatusCode)
	case http.StatusRequestedRangeNotSatisfiable:
		// The server holds fewer bytes than we do. Start over rather than loop on it.
		_ = os.Remove(opts.Part)
		return fmt.Errorf("%w: HTTP 416 at offset %d", ErrInterrupted, held)
	default:
		return fmt.Errorf("%w: HTTP %d", ErrInterrupted, resp.StatusCode)
	}

	f, err := os.OpenFile(opts.Part, flags, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Never take more than the assignment declared: read one byte past the remainder so that an
	// over-long body is detected rather than silently truncated to look right.
	remaining := opts.Size - held
	r := io.LimitReader(resp.Body, remaining+1)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			stall.Reset(idle)
			if int64(n) > remaining {
				_ = f.Close()
				_ = os.Remove(opts.Part)
				return fmt.Errorf("%w: the body is longer than the %d declared bytes", ErrSizeMismatch, opts.Size)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
			held += int64(n)
			if opts.OnBytes != nil {
				opts.OnBytes(held)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Sync()
			return fmt.Errorf("%w: %v", ErrInterrupted, redact(rerr))
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if held != opts.Size {
		return fmt.Errorf("%w: the body ended at %d of %d bytes", ErrInterrupted, held, opts.Size)
	}
	return nil
}

// redact strips the request URL, which carries the capability, from a transport error.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, stripURL(ue.Err))
	}
	return stripURL(err)
}

var urlInText = regexp.MustCompile(`https?://\S+`)

func stripURL(err error) error {
	msg := urlInText.ReplaceAllString(err.Error(), "<url>")
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}
