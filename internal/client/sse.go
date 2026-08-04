package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SSEEvent is one server-sent event; Data carries the raw JSON payload.
type SSEEvent struct {
	Event string
	ID    string
	Data  string
}

// Stream opens one SSE subscription and delivers events until the context
// ends or the server closes (the channel closes either way; servers close
// on overflow and expect a reconnect). The stream uses an untimed client:
// the 15 second request timeout would sever it.
func (c *Client) Stream(ctx context.Context, path string, lastEventID string) (<-chan SSEEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	res, err := c.streaming.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	if err := c.checkInstance(res); err != nil {
		res.Body.Close()
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		return nil, decodeErrorEnvelope(res.StatusCode, raw)
	}

	events := make(chan SSEEvent, 64)
	go func() {
		defer close(events)
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var current SSEEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if current.Data != "" || current.Event != "" {
					select {
					case events <- current:
					case <-ctx.Done():
						return
					}
				}
				current = SSEEvent{}
			case strings.HasPrefix(line, "event:"):
				current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "id:"):
				current.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "data:"):
				payload := strings.TrimPrefix(line, "data:")
				payload = strings.TrimPrefix(payload, " ")
				if current.Data != "" {
					current.Data += "\n"
				}
				current.Data += payload
			case strings.HasPrefix(line, ":"):
				// Comment ping; ignored.
			}
		}
	}()
	return events, nil
}
