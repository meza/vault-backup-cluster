package schedule

import "time"

type Interval struct {
	every time.Duration
}

func New(every time.Duration) Interval {
	return Interval{every: every}
}

func (i Interval) Next(now time.Time) time.Time {
	if i.every <= 0 {
		return now
	}
	utc := now.UTC()
	base := utc.Truncate(i.every)
	if base.Equal(utc) {
		return utc.Add(i.every)
	}
	return base.Add(i.every)
}
