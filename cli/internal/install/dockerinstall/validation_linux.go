package dockerinstall

import (
	"strings"
	"syscall"
)

func defaultUseGateway() bool {
	return isRunningInWSL()
}

func isRunningInWSL() bool {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return false
	}

	containsMicrosoft := func(ca []int8) bool {
		return strings.Contains(strings.ToLower(charsToString(ca)), "microsoft")
	}

	return containsMicrosoft(uts.Version[:]) || containsMicrosoft(uts.Release[:])
}

func charsToString(ca []int8) string {
	s := make([]byte, len(ca))
	for i, c := range ca {
		if c == 0 {
			return string(s[:i]) // Null-terminated string
		}
		s[i] = byte(c)
	}
	return string(s)
}
