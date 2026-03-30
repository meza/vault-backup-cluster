package schedule

import (
"testing"
"time"
)

func TestNextAlignsToIntervalBoundary(t *testing.T) {
schedule := New(15 * time.Minute)
now := time.Date(2026, time.March, 30, 11, 7, 10, 0, time.UTC)

next := schedule.Next(now)
want := time.Date(2026, time.March, 30, 11, 15, 0, 0, time.UTC)
if !next.Equal(want) {
t.Fatalf("expected %s, got %s", want, next)
}
}

func TestNextAdvancesWhenAlreadyOnBoundary(t *testing.T) {
schedule := New(5 * time.Minute)
now := time.Date(2026, time.March, 30, 11, 10, 0, 0, time.UTC)

next := schedule.Next(now)
want := time.Date(2026, time.March, 30, 11, 15, 0, 0, time.UTC)
if !next.Equal(want) {
t.Fatalf("expected %s, got %s", want, next)
}
}
