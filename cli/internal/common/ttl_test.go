// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package common

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTimeToLive(t *testing.T) {
	t.Parallel()

	validTests := []struct {
		input           string
		expectedDays    int
		expectedHours   int
		expectedMinutes int
		expectedSeconds int
	}{
		// Days only
		{"0", 0, 0, 0, 0},
		{"3", 3, 0, 0, 0},
		{"14", 14, 0, 0, 0},
		{"365", 365, 0, 0, 0},

		// Days and hours
		{"0.01", 0, 1, 0, 0},
		{"1.12", 1, 12, 0, 0},
		{"0.01", 0, 1, 0, 0},
		{"2.23", 2, 23, 0, 0},

		// Days, hours, and minutes
		{"0.00:00", 0, 0, 0, 0},
		{"1.01:01", 1, 1, 1, 0},
		{"23.23:23", 23, 23, 23, 0},

		// Days, hours, minutes, seconds
		{"0.00:00:00", 0, 0, 0, 0},
		{"1.01:01:01", 1, 1, 1, 1},
		{"1.02:03:30", 1, 2, 3, 30},
		{"1.23:59:59", 1, 23, 59, 59},

		// Ignore leading/trailing whitespace
		{"1.23:59:59  ", 1, 23, 59, 59},
		{"  1.23:59:59", 1, 23, 59, 59},
	}

	toStringRegex := regexp.MustCompile(`^\d+.\d\d:\d\d:\d\d$`)

	for _, tc := range validTests {
		t.Run("Valid: "+tc.input, func(t *testing.T) {
			ttl, err := ParseTimeToLive(tc.input)
			require.Nil(t, err)

			require.Equal(t, tc.expectedDays, ttl.Days, "days")
			require.Equal(t, tc.expectedHours, ttl.Hours, "hours")
			require.Equal(t, tc.expectedMinutes, ttl.Minutes, "minutes")
			require.Equal(t, tc.expectedSeconds, ttl.Seconds, "seconds")

			// Verify the string representation
			require.True(t, toStringRegex.MatchString(ttl.String()))

			// Verify the Duration representation
			totalMillis := 1000 * (int64(ttl.Days)*24*60*60 + int64(ttl.Hours)*60*60 + int64(ttl.Minutes*60) + int64(ttl.Seconds))
			require.Equal(t, ttl.ToDuration().Milliseconds(), totalMillis)
		})
	}

	invalidTests := []string{
		// Invalid characters
		"-1",
		"1d",
		"-1.00:00:00",
		"1:00:00:00",

		// Missing values
		".00",
		".00:00",
		"1.",
		"1.",
		"1.00:",
		"1.00:00:",

		// Incorrect segment format
		"1.0",
		"1.000",
		"1.0:00",
		"1.00:0",
		"1.00:000",
		"1.00:00:0",
		"1.00:00:000",

		// Out of range values
		"1.24",       // Hours > 23
		"4.55",       // Hours > 23
		"2.00:60",    // Minutes > 59
		"2.00:99",    // Minutes > 59
		"1.00:00:60", // Seconds > 59
		"1.23:45:67", // Seconds > 59
	}

	for _, input := range invalidTests {
		t.Run("Invalid: "+input, func(t *testing.T) {
			_, err := ParseTimeToLive(input)
			require.NotNil(t, err)
		})
	}
}
