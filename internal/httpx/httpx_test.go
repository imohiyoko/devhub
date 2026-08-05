package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decode(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.String(), err)
	}
	return m
}

// A plain error and a hint-less HTTPError must serialize exactly as they did
// before Code/Hint existed. Every tool page reads data.error and nothing else,
// so an unconditional code/hint key would be a silent contract change.
func TestWriteErrorOmitsUnsetCodeAndHint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"plain error", errors.New("boom"), http.StatusBadRequest},
		{"HTTPError without hint", Errorf(http.StatusConflict, "boom"), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			WriteError(rr, tc.err)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			m := decode(t, rr)
			if m["error"] != "boom" {
				t.Errorf("error = %v, want boom", m["error"])
			}
			if len(m) != 1 {
				t.Errorf("body has %d keys (%v), want only \"error\"", len(m), m)
			}
		})
	}
}

func TestWriteErrorEmitsCodeAndHint(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, Errorf(http.StatusRequestTimeout, "approval timed out").
		WithHint("approval_timeout", "Ask the user to approve, then retry."))

	if rr.Code != http.StatusRequestTimeout {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusRequestTimeout)
	}
	m := decode(t, rr)
	if m["error"] != "approval timed out" {
		t.Errorf("error = %v", m["error"])
	}
	if m["code"] != "approval_timeout" {
		t.Errorf("code = %v, want approval_timeout", m["code"])
	}
	if m["hint"] != "Ask the user to approve, then retry." {
		t.Errorf("hint = %v", m["hint"])
	}
}

// Extra is merged first so the typed fields stay authoritative; a caller that
// sets both must not get the map's copy.
func TestWriteErrorTypedFieldsBeatExtra(t *testing.T) {
	err := Errorf(http.StatusBadRequest, "boom").WithHint("real_code", "real hint")
	err.Extra = map[string]any{"code": "stale_code", "detail": "kept"}

	rr := httptest.NewRecorder()
	WriteError(rr, err)

	m := decode(t, rr)
	if m["code"] != "real_code" {
		t.Errorf("code = %v, want real_code", m["code"])
	}
	if m["detail"] != "kept" {
		t.Errorf("unrelated Extra key dropped: %v", m)
	}
}

// WithHint returns its receiver so it can be chained onto Errorf.
func TestWithHintReturnsSameError(t *testing.T) {
	base := Errorf(http.StatusForbidden, "no")
	if got := base.WithHint("c", "h"); got != base {
		t.Errorf("WithHint returned a different pointer")
	}
}
