// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tygerproxy

import "testing"

func TestUrlsEquivalent(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"identical", "http://myserver:8080", "http://myserver:8080", true},
		{"scheme casing", "http://myserver:8080", "HTTP://myserver:8080", true},
		{"host casing", "http://MyServer:8080", "http://myserver:8080", true},
		{"mixed case scheme and host", "HTTPS://MyServer.Example.COM/api", "https://myserver.example.com/api", true},
		{"escaped vs unescaped brackets in query",
			"ssh://user@myhost/opt/tyger/api.sock?option[StrictHostKeyChecking]=no&option[UserKnownHostsFile]=NUL",
			"ssh://user@myhost/opt/tyger/api.sock?option%5BStrictHostKeyChecking%5D=no&option%5BUserKnownHostsFile%5D=NUL",
			true},
		{"order of query parameters doesn't matter",
			"ssh://user@myhost/opt/tyger/api.sock?option[StrictHostKeyChecking]=no&option[UserKnownHostsFile]=NUL",
			"ssh://user@myhost/opt/tyger/api.sock?option[UserKnownHostsFile]=NUL&option[StrictHostKeyChecking]=no",
			true},
		{"escaped vs unescaped brackets reversed",
			"ssh://user@myhost/opt/tyger/api.sock?option%5BStrictHostKeyChecking%5D=no&option%5BUserKnownHostsFile%5D=NUL",
			"ssh://user@myhost/opt/tyger/api.sock?option[StrictHostKeyChecking]=no&option[UserKnownHostsFile]=NUL",
			true},
		{"different servers", "http://server-a:8080", "http://server-b:8080", false},
		{"different ports", "http://myserver:8080", "http://myserver:9090", false},
		{"different schemes", "http://myserver:8080", "https://myserver:8080", false},
		{"different query values",
			"ssh://user@myhost/opt/tyger/api.sock?option[StrictHostKeyChecking]=no",
			"ssh://user@myhost/opt/tyger/api.sock?option[StrictHostKeyChecking]=yes",
			false},
		{"different paths", "http://myserver:8080/a", "http://myserver:8080/b", false},
		{"trailing slash", "http://myserver:8080/api/", "http://myserver:8080/api", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := urlsEquivalent(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("UrlsEquivalent(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
