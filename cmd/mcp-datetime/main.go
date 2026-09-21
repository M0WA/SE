// Command mcp-datetime is a first-party MCP (Model Context Protocol) server
// exposing a single "get_datetime" tool that reports the current date and
// time -- the replacement for the old %c/strftime prompt-placeholder
// mechanism, which has been removed entirely: rather than the server
// always injecting a formatted timestamp into the system prompt on every
// turn (whether or not the model actually needed one), the model now calls
// this tool only when it actually needs to reason about "now," exactly the
// same on-demand pattern web_search/web_fetch (cmd/mcp-web) already use.
// Spawned as a stdio subprocess by internal/adapters/mcpclient (see
// domain.MCPServer's Transport="stdio" configuration) rather than run as a
// systemd service.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getDatetimeArgs struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"IANA timezone name, e.g. 'Europe/Berlin' or 'America/New_York' -- optional, defaults to UTC. An unrecognized name falls back to UTC rather than erroring, since guessing the current time in SOME zone is more useful to the model than failing the call outright."`
}

func main() {
	server := newServer()
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the mcp.Server exposing "get_datetime", factored out of
// main so a test can connect to it directly over an in-memory transport
// (mcp.NewInMemoryTransports) instead of exercising it only via a real
// stdio subprocess.
func newServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-datetime", Version: "1"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_datetime",
		Description: "Returns the current date and time. Use this whenever you need to know what day, date, " +
			"or time it currently is, or how long ago/until some date is -- your own training data has a " +
			"cutoff and does not know the current date on its own, even if you feel confident.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args getDatetimeArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: getDatetime(args.Timezone)}}}, nil, nil
	})

	return server
}

// getDatetime renders the current time as a small, self-describing JSON
// object (the same "raw JSON text" convention web_search/web_fetch use) --
// utc/unix are always present and zone-independent, so the model always has
// an unambiguous anchor even when timezone is empty/unrecognized; formatted
// carries a human-readable rendering in whichever zone actually applied
// (see resolveZone), with that zone's own name/offset included so the model
// never has to guess which one it got.
func getDatetime(timezone string) string {
	loc, zoneName := resolveZone(timezone)
	now := time.Now().UTC()
	local := now.In(loc)
	_, offsetSeconds := local.Zone()
	return fmt.Sprintf(
		`{"utc":%q,"unix":%d,"timezone":%q,"utc_offset":%q,"formatted":%q}`,
		now.Format(time.RFC3339),
		now.Unix(),
		zoneName,
		formatUTCOffset(offsetSeconds),
		local.Format("Mon Jan 2 15:04:05 2006"),
	)
}

// resolveZone loads timezone via Go's own IANA tzdata lookup, falling back
// to UTC (with an explicit "UTC" name, not an empty string) for an empty or
// unrecognized name -- see getDatetimeArgs.Timezone's own doc comment for
// why this degrades gracefully instead of erroring the whole call.
func resolveZone(timezone string) (*time.Location, string) {
	if timezone == "" {
		return time.UTC, "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC, "UTC"
	}
	return loc, timezone
}

// formatUTCOffset renders a zone offset as "+02:00"/"-05:00"/"+00:00" --
// the same sign-hours-minutes shape RFC3339 uses for its own numeric
// offset, so the model sees a familiar format rather than a bare seconds
// count.
func formatUTCOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offsetSeconds/3600, (offsetSeconds%3600)/60)
}
