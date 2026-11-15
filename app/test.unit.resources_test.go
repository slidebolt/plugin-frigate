package app

import (
	"errors"
	"testing"

	runtime "github.com/slidebolt/sb-runtime"
)

type resourceEvent struct {
	id    string
	state string
}

type resourceRecorder struct{ events []resourceEvent }

func (r *resourceRecorder) Ready(id string) { r.events = append(r.events, resourceEvent{id, "ready"}) }
func (r *resourceRecorder) Degraded(id, _, _ string, _ bool) {
	r.events = append(r.events, resourceEvent{id, "degraded"})
}

var _ runtime.ResourceReporter = (*resourceRecorder)(nil)

func TestResourceTransitions(t *testing.T) {
	recorder := &resourceRecorder{}
	a := New()
	a.SetResourceReporter(recorder)
	a.resourceReady(frigateAPIResource)
	a.resourceDegraded(frigateAPIResource, "api_unavailable", errors.New("offline"))
	if len(recorder.events) != 2 || recorder.events[0] != (resourceEvent{frigateAPIResource, "ready"}) || recorder.events[1] != (resourceEvent{frigateAPIResource, "degraded"}) {
		t.Fatalf("events = %+v", recorder.events)
	}
}
