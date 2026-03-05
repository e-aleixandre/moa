package anthropic

import (
	"bufio"
	"io"
	"strings"
)

// parseSSEFrames reads an SSE stream properly:
//   - Accumulates multiple "data:" lines per frame
//   - Delimited by blank lines (\n\n)
//   - Handles keep-alive comments (lines starting with ":")
//   - Uses bufio.Reader with 1MB buffer (no 64KB Scanner limit)
//
// onFrame is called for each complete SSE frame with the event type and data.
// The eventType may be empty if no "event:" field was present.
func parseSSEFrames(r io.Reader, onFrame func(eventType, data string)) error {
	reader := bufio.NewReaderSize(r, 1024*1024) // 1MB buffer

	var eventType string
	var dataLines []string

	for {
		line, err := readLine(reader)
		if err != nil {
			if err == io.EOF {
				// Flush any remaining frame
				if len(dataLines) > 0 {
					onFrame(eventType, strings.Join(dataLines, "\n"))
				}
				return nil
			}
			return err
		}

		// Blank line = end of frame
		if line == "" {
			if len(dataLines) > 0 {
				onFrame(eventType, strings.Join(dataLines, "\n"))
				eventType = ""
				dataLines = dataLines[:0]
			}
			continue
		}

		// Comment (keep-alive)
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse field
		if colonIdx := strings.IndexByte(line, ':'); colonIdx >= 0 {
			field := line[:colonIdx]
			value := line[colonIdx+1:]
			// SSE spec: if value starts with a space, strip it
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}

			switch field {
			case "event":
				eventType = value
			case "data":
				dataLines = append(dataLines, value)
			}
			// Other fields (id, retry) are ignored for our purposes
		}
	}
}

// readLine reads a single line, handling lines that may exceed the buffer.
// Returns the line without the trailing \n or \r\n.
func readLine(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		segment, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		sb.Write(segment)
		if !isPrefix {
			return sb.String(), nil
		}
	}
}
