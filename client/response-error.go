package client

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ResponseError retains the Bridge status and requested retry delay.
type ResponseError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *ResponseError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func retryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 && seconds <= 86400 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}
