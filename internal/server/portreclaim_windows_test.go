//go:build windows

package server

import "testing"

func TestTasklistImageName(t *testing.T) {
	cases := map[string]string{
		"\"devhub.exe\",\"1234\",\"Console\",\"1\",\"10,000 K\"\r\n":         "devhub",
		"\"DEVHUB.EXE\",\"1234\",\"Console\",\"1\",\"10,000 K\"\r\n":         "devhub",
		"\"go.exe\",\"99\",\"Console\",\"1\",\"1,000 K\"\r\n":                "go",
		"INFO: No tasks are running which match the specified criteria.\r\n": "",
		"": "",
	}
	for in, want := range cases {
		if got := tasklistImageName(in); got != want {
			t.Errorf("tasklistImageName(%q) = %q, want %q", in, got, want)
		}
	}
}
