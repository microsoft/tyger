// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package common

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var TimeSpanRegex = regexp.MustCompile(`^(\d+)([.](\d\d)(:(\d\d)(:(\d\d))?)?)?$`)

// TimeToLive represents a Duration including days
type TimeToLive struct {
	Days    int
	Hours   int
	Minutes int
	Seconds int
}

func (ttl TimeToLive) String() string {
	return fmt.Sprintf("%d.%02d:%02d:%02d", ttl.Days, ttl.Hours, ttl.Minutes, ttl.Seconds)
}

func (ttl TimeToLive) ToDuration() time.Duration {
	return time.Duration(ttl.Days)*24*time.Hour +
		time.Duration(ttl.Hours)*time.Hour +
		time.Duration(ttl.Minutes)*time.Minute +
		time.Duration(ttl.Seconds)*time.Second
}

// ParseTimeToLive parses a string in the format "D.HH:MM:SS" into a TimeToLive struct.
// The string can also be in the format "D.HH:MM" or "D.HH" or just "D".
func ParseTimeToLive(ttl string) (TimeToLive, error) {
	ttl = strings.TrimSpace(ttl)

	result := TimeToLive{}

	groups := TimeSpanRegex.FindStringSubmatch(ttl)
	if groups == nil {
		return result, fmt.Errorf("invalid TTL format: '%s'", ttl)
	}

	parseNonNegativeBoundedInteger := func(s string, maxAllowed int) (int, error) {
		if s == "" {
			return 0, nil
		}

		n, err := strconv.Atoi(s)
		if err != nil {
			return -1, err
		}

		if n < 0 || n > maxAllowed {
			return -1, fmt.Errorf("value out of range: %d (max: %d)", n, maxAllowed)
		}

		return n, nil
	}

	days, err := parseNonNegativeBoundedInteger(groups[1], math.MaxInt)
	if err != nil {
		return result, fmt.Errorf("invalid TTL format: '%s'", ttl)
	}

	hours, err := parseNonNegativeBoundedInteger(groups[3], 23)
	if err != nil {
		return result, fmt.Errorf("invalid hours in TTL format: '%s'", ttl)
	}

	minutes, err := parseNonNegativeBoundedInteger(groups[5], 59)
	if err != nil {
		return result, fmt.Errorf("invalid minutes in TTL format: '%s'", ttl)
	}

	seconds, err := parseNonNegativeBoundedInteger(groups[7], 59)
	if err != nil {
		return result, fmt.Errorf("invalid seconds TTL format: '%s'", ttl)
	}

	result.Days = days
	result.Hours = hours
	result.Minutes = minutes
	result.Seconds = seconds

	return result, nil
}
