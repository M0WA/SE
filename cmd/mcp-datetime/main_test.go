package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectedTestServer wires newServer's mcp.Server to a real mcp.Client
// over an in-memory transport, mirroring cmd/mcp-web's own test helper --
// exercises the tool exactly as a real chat turn would, through CallTool,
// not by calling getDatetime directly.
func connectedTestServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := newServer()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d: %+v", len(result.Content), result.Content)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return tc.Text
}

func TestGetDatetimeTool_DefaultsToUTC(t *testing.T) {
	before := time.Now().UTC()
	cs := connectedTestServer(t)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_datetime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	after := time.Now().UTC()

	var parsed struct {
		UTC       string `json:"utc"`
		Unix      int64  `json:"unix"`
		Timezone  string `json:"timezone"`
		UTCOffset string `json:"utc_offset"`
		Formatted string `json:"formatted"`
	}
	if err := json.Unmarshal([]byte(textContent(t, result)), &parsed); err != nil {
		t.Fatalf("expected valid JSON content, got %q: %v", textContent(t, result), err)
	}
	if parsed.Timezone != "UTC" {
		t.Errorf("expected timezone UTC by default, got %q", parsed.Timezone)
	}
	if parsed.UTCOffset != "+00:00" {
		t.Errorf("expected +00:00 offset for UTC, got %q", parsed.UTCOffset)
	}
	got, err := time.Parse(time.RFC3339, parsed.UTC)
	if err != nil {
		t.Fatalf("expected utc field to be a valid RFC3339 timestamp, got %q: %v", parsed.UTC, err)
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("expected utc %v to fall between %v and %v", got, before, after)
	}
	if parsed.Unix != got.Unix() {
		t.Errorf("expected unix field to match the utc field, got unix=%d utc=%v", parsed.Unix, got)
	}
}

func TestGetDatetimeTool_RecognizedTimezone(t *testing.T) {
	cs := connectedTestServer(t)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_datetime", Arguments: map[string]any{"timezone": "Europe/Berlin"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Timezone  string `json:"timezone"`
		UTCOffset string `json:"utc_offset"`
	}
	if err := json.Unmarshal([]byte(textContent(t, result)), &parsed); err != nil {
		t.Fatalf("expected valid JSON content: %v", err)
	}
	if parsed.Timezone != "Europe/Berlin" {
		t.Errorf("expected the requested timezone echoed back, got %q", parsed.Timezone)
	}
	// Berlin is UTC+1 or UTC+2 depending on DST -- either is a valid,
	// correctly-formatted positive offset; this just proves it's not the
	// UTC fallback (+00:00).
	if parsed.UTCOffset == "+00:00" {
		t.Errorf("expected a non-UTC offset for Europe/Berlin, got %q", parsed.UTCOffset)
	}
}

func TestGetDatetimeTool_UnrecognizedTimezoneFallsBackToUTC(t *testing.T) {
	cs := connectedTestServer(t)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_datetime", Arguments: map[string]any{"timezone": "Not/AZone"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected a graceful UTC fallback, not a tool error: %s", textContent(t, result))
	}
	if !strings.Contains(textContent(t, result), `"timezone":"UTC"`) {
		t.Errorf("expected an unrecognized timezone to fall back to UTC, got %q", textContent(t, result))
	}
}

func TestResolveZone(t *testing.T) {
	if loc, name := resolveZone(""); loc != time.UTC || name != "UTC" {
		t.Errorf("expected UTC for an empty timezone, got %v/%q", loc, name)
	}
	if loc, name := resolveZone("bogus"); loc != time.UTC || name != "UTC" {
		t.Errorf("expected UTC for an unrecognized timezone, got %v/%q", loc, name)
	}
	loc, name := resolveZone("America/New_York")
	if name != "America/New_York" {
		t.Errorf("expected the recognized timezone name echoed back, got %q", name)
	}
	if loc == time.UTC {
		t.Error("expected a real *time.Location for a recognized timezone, not the UTC fallback")
	}
}

func TestFormatUTCOffset(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{0, "+00:00"},
		{3600, "+01:00"},
		{-3600, "-01:00"},
		{19800, "+05:30"},
		{-28800, "-08:00"},
	}
	for _, c := range cases {
		if got := formatUTCOffset(c.seconds); got != c.want {
			t.Errorf("formatUTCOffset(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestGetDatetime_ProducesValidJSON(t *testing.T) {
	out := getDatetime("")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", out, err)
	}
	for _, field := range []string{"utc", "unix", "timezone", "utc_offset", "formatted"} {
		if _, ok := parsed[field]; !ok {
			t.Errorf("expected field %q in the output, got %v", field, parsed)
		}
	}
}
