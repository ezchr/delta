package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogsEncodeAndDrop(t *testing.T) {
	var l Logs
	l.Add(Info, "a\nb %d", 1)
	l.Add(Debug, "hidden")
	if got := Encode(l.Drain()); got != "INFO\ta\\nb 1\n" {
		t.Fatalf("%q", got)
	}
	for i := 0; i < maxQueued+10; i++ {
		l.Add(Info, "x")
	}
	recs := l.Drain()
	if len(recs) > maxQueued+1 || recs[0].Level != Warn || !strings.Contains(recs[0].Msg, "dropped") {
		t.Fatalf("%d records, first %+v", len(recs), recs[0])
	}
}

type fakeService struct{ stopped bool }

func (f *fakeService) Stop(time.Time) error { f.stopped = true; return nil }
func (f *fakeService) Call(m string, _ json.RawMessage) (any, error) {
	if m == "boom" {
		panic("boom")
	}
	return m, nil
}

func TestStartStopCallPanics(t *testing.T) {
	svc := &fakeService{}
	Register(func(json.RawMessage, *Logs) (Service, error) { return svc, nil })
	if err := Start([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := Start([]byte(`{}`)); err != ErrRunning {
		t.Fatalf("second start: %v", err)
	}
	if got := string(Call([]byte(`{"method":"echo"}`))); got != `{"ok":true,"result":"echo"}` {
		t.Fatalf("call: %s", got)
	}
	if got := string(Call([]byte(`{"method":"boom"}`))); !strings.Contains(got, `"ok":false`) {
		t.Fatalf("panicking call: %s", got)
	}
	done := make(chan any, 1)
	Go("test", func() { panic("in a goroutine") }, func(r any) { done <- r })
	if r := <-done; r != "in a goroutine" {
		t.Fatalf("recovered %v", r)
	}
	if err := Stop(time.Second); err != nil || !svc.stopped || Running() {
		t.Fatalf("stop: %v stopped %v running %v", err, svc.stopped, Running())
	}
	Register(func(json.RawMessage, *Logs) (Service, error) { panic("factory") })
	if err := Start(nil); err == nil || !strings.Contains(LastError(), "panic") {
		t.Fatalf("panicking start: %v", err)
	}
}
