// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFetchPodLogsReturnsPartialLogsAndCombinedErrors(t *testing.T) {
	pods := []v1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ok"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "get-fails"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "read-fails"}},
	}

	logs, err := fetchPodLogs(context.Background(), pods, func(_ context.Context, podName string) ([]byte, error) {
		switch podName {
		case "ok":
			return []byte("ok logs"), nil
		case "read-fails":
			return []byte("partial logs"), errors.New("read failed")
		default:
			return nil, errors.New("get failed")
		}
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `pod "get-fails": get failed`)
	assert.Contains(t, err.Error(), `pod "read-fails": read failed`)
	assert.Equal(t, []byte("ok logs"), logs["ok"])
	assert.Equal(t, []byte("partial logs"), logs["read-fails"])
	assert.NotContains(t, logs, "get-fails")
}
