package storage

import (
	"testing"

	devhub "github.com/imohiyoko/devhub"
)

func TestActiveInstanceRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := ActiveInstance{Port: 9000, PID: 1234, Instance: "instance-1"}
	if err := st.RecordActiveInstance(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadActiveInstance()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("active instance = %#v, want %#v", got, want)
	}
}
