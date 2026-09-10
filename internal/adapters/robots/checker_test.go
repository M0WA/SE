package robots_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/adapters/robots"
)

type fakeFetcher struct {
	body  string
	err   error
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string) (string, error) {
	f.calls++
	return f.body, f.err
}

func TestChecker_Allowed_NoRobotsTxt(t *testing.T) {
	c := robots.New(&fakeFetcher{err: errors.New("404")})
	if !c.Allowed(context.Background(), "https://x.example/anything") {
		t.Error("expected allowed when robots.txt unreachable")
	}
}

func TestChecker_Allowed_DisallowedPath(t *testing.T) {
	body := "User-agent: *\nDisallow: /private\n"
	c := robots.New(&fakeFetcher{body: body})
	if c.Allowed(context.Background(), "https://x.example/private/page") {
		t.Error("expected /private disallowed")
	}
	if !c.Allowed(context.Background(), "https://x.example/public") {
		t.Error("expected /public allowed")
	}
}

func TestChecker_Allowed_IgnoresOtherAgents(t *testing.T) {
	body := "User-agent: Googlebot\nDisallow: /\nUser-agent: *\nDisallow:\n"
	c := robots.New(&fakeFetcher{body: body})
	if !c.Allowed(context.Background(), "https://x.example/anything") {
		t.Error("expected *-group (empty Disallow) allows everything")
	}
}

func TestChecker_Allowed_CachesPerOrigin(t *testing.T) {
	f := &fakeFetcher{body: "User-agent: *\nDisallow: /x\n"}
	c := robots.New(f)
	c.Allowed(context.Background(), "https://x.example/a")
	c.Allowed(context.Background(), "https://x.example/b")
	if f.calls != 1 {
		t.Errorf("expected robots.txt loaded only once (cache), got %d calls", f.calls)
	}
}

func TestChecker_Allowed_InvalidURL(t *testing.T) {
	c := robots.New(&fakeFetcher{})
	if c.Allowed(context.Background(), "://ungueltig") {
		t.Error("expected invalid URL treated as disallowed")
	}
}
