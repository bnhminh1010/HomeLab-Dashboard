package operations

import (
	"errors"
	"testing"
	"time"
)

func TestValidateEventAllowsAutomaticAndManualTimelineRecords(t *testing.T) {
	automatic := Event{
		Type: EventServiceHealthChanged, Source: SourceAutomatic,
		Title: "Immich probe changed to down", ServiceID: "immich", NodeID: "local",
	}
	if err := ValidateEvent(automatic); err != nil {
		t.Fatalf("automatic event = %v", err)
	}
	manual := Event{Type: EventDeploy, Source: SourceManual, Title: "Deployed immich v1.132.0", Actor: "admin@example.com"}
	if err := ValidateEvent(manual); err != nil {
		t.Fatalf("manual event = %v", err)
	}
}

func TestValidateEventRejectsUnsafeAndUnsupportedManualRecords(t *testing.T) {
	for _, event := range []Event{
		{Type: "service health", Source: SourceAutomatic, Title: "Invalid type"},
		{Type: EventServiceHealthChanged, Source: SourceManual, Title: "Unsupported manual event"},
		{Type: EventNote, Source: SourceManual, Title: "A\nmultiline note"},
		{Type: EventNote, Source: "other", Title: "Unsupported source"},
	} {
		if err := ValidateEvent(event); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("event %#v error = %v, want ErrInvalidEvent", event, err)
		}
	}
}

func TestNormalizeFilterBoundsAndValidatesRange(t *testing.T) {
	filter, err := NormalizeFilter(Filter{Limit: 9999})
	if err != nil || filter.Limit != MaxListLimit {
		t.Fatalf("filter = %#v, err = %v", filter, err)
	}
	_, err = NormalizeFilter(Filter{From: time.Now(), To: time.Now().Add(-time.Minute)})
	if !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("range err = %v, want ErrInvalidFilter", err)
	}
}
