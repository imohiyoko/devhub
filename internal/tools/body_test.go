package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imohiyoko/devhub/internal/httpx"
)

func TestDecodeBodyRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty", "", http.StatusBadRequest},
		{"malformed", `{"environments":[}`, http.StatusBadRequest},
		{"null", "null", http.StatusBadRequest},
		{"array", "[]", http.StatusBadRequest},
		{"multiple values", `{} {}`, http.StatusBadRequest},
		{"oversized", `{"value":"` + strings.Repeat("x", maxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
		{"valid object padded over limit", `{}` + strings.Repeat(" ", maxBodyBytes), http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/envs", strings.NewReader(tt.body))
			_, err := decodeBody(httptest.NewRecorder(), req)
			if err == nil {
				t.Fatal("decodeBody accepted invalid input")
			}
			he, ok := err.(*httpx.HTTPError)
			if !ok || he.Status != tt.want {
				t.Fatalf("error = %#v, want HTTP %d", err, tt.want)
			}
		})
	}
}

func TestDecodeBodyAcceptsOneObject(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/envs", strings.NewReader(`{"environments":[]}`))
	got, err := decodeBody(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["environments"].([]any); !ok {
		t.Fatalf("decoded body = %#v", got)
	}
}
