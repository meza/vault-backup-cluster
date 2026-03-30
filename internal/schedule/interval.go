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
	return utc.Truncate(i.every).Add(i.every)
}
