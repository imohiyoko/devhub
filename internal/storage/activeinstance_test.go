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

func TestClearActiveInstanceDoesNotEraseReplacement(t *testing.T) {
	st, err := Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := ActiveInstance{Port: 8765, PID: 1234, Instance: "old"}
	current := ActiveInstance{Port: 9000, PID: 5678, Instance: "current"}
	if err := st.RecordActiveInstance(old); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActiveInstance(current); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearActiveInstance(old); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadActiveInstance(); err != nil || got != current {
		t.Fatalf("after clearing old: got %#v, %v; want %#v", got, err, current)
	}
	if err := st.ClearActiveInstance(current); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadActiveInstance(); err != nil || got != (ActiveInstance{}) {
		t.Fatalf("after clearing current: got %#v, %v; want empty", got, err)
	}
}
