// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
)

func ParseJsonLogs(logs []byte) ([]map[string]any, error) {
	parsedLines := make([]map[string]any, 0)
	scanner := bufio.NewScanner(bytes.NewReader(logs))
	for scanner.Scan() {
		line := scanner.Text()
		var parsedLine map[string]any

		if err := json.Unmarshal([]byte(line), &parsedLine); err != nil {
			continue
		}
		parsedLines = append(parsedLines, parsedLine)

	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to split logsResult into lines: %w", err)
	}

	return parsedLines, nil
}
