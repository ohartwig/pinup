// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"bytes"
	"io"
	"net/http"
)

// responseRecorder is a minimal http.ResponseWriter that turns a handler's
// output back into an http.Response. httptest has one, but it is in a package
// tests import and this transport is used from non-test helpers too; the
// implementation is small enough not to be worth the coupling.
type responseRecorder struct {
	code   int
	header http.Header
	body   bytes.Buffer
}

func newRecorder() *responseRecorder {
	return &responseRecorder{code: 0, header: http.Header{}}
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func (r *responseRecorder) result(req *http.Request) *http.Response {
	code := r.code
	if code == 0 {
		code = http.StatusOK
	}
	body := r.body.Bytes()
	return &http.Response{
		Status:        http.StatusText(code),
		StatusCode:    code,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        r.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
