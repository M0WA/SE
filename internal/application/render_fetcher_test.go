package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeAuthFetcher is a minimal ports.AuthFetcher -- records the options it
// was called with, for assertions on what RenderAwareFetcher passed through.
type fakeAuthFetcher struct {
	gotURL  string
	gotOpts ports.FetchOptions
	body    string
	err     error
	calls   int
}

func (f *fakeAuthFetcher) FetchWithOptions(_ context.Context, url string, opts ports.FetchOptions) (string, error) {
	f.calls++
	f.gotURL, f.gotOpts = url, opts
	return f.body, f.err
}

// fakeRenderer is a minimal ports.Renderer.
type fakeRenderer struct {
	gotURL  string
	gotOpts ports.FetchOptions
	body    string
	err     error
	calls   int
}

func (f *fakeRenderer) Render(_ context.Context, url string, opts ports.FetchOptions) (string, error) {
	f.calls++
	f.gotURL, f.gotOpts = url, opts
	return f.body, f.err
}

func TestRenderAwareFetcher_NoRendererConfiguredUsesPlainFetch(t *testing.T) {
	base := &fakeAuthFetcher{body: "plain html"}
	f := &application.RenderAwareFetcher{Base: base, OpSettings: domain.DefaultOperationalSettings()}

	html, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "plain html" || base.calls != 1 {
		t.Errorf("expected the plain fetcher used, got html=%q calls=%d", html, base.calls)
	}
}

func TestRenderAwareFetcher_PerFetchRendererOverridesGlobalDefault(t *testing.T) {
	base := &fakeAuthFetcher{}
	chromium := &fakeRenderer{body: "rendered html"}
	opSettings := domain.DefaultOperationalSettings() // global default stays "none"
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: opSettings,
		Renderers: map[string]ports.Renderer{domain.RendererChromium: chromium},
	}

	html, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{Renderer: domain.RendererChromium})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "rendered html" || chromium.calls != 1 || base.calls != 0 {
		t.Errorf("expected chromium renderer used instead of the plain fetcher, got html=%q chromium.calls=%d base.calls=%d", html, chromium.calls, base.calls)
	}
	if chromium.gotURL != "http://a" {
		t.Errorf("expected the URL passed through, got %q", chromium.gotURL)
	}
}

func TestRenderAwareFetcher_EmptyRendererInheritsGlobalDefault(t *testing.T) {
	base := &fakeAuthFetcher{}
	firefox := &fakeRenderer{body: "rendered html"}
	opValues := domain.DefaultOperationalSettings().Get()
	opValues.DefaultRenderer = domain.RendererFirefox
	opSettings := domain.NewOperationalSettings(opValues)
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: opSettings,
		Renderers: map[string]ports.Renderer{domain.RendererFirefox: firefox},
	}

	// opts.Renderer left blank -- must inherit the global default (firefox).
	if _, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firefox.calls != 1 || base.calls != 0 {
		t.Errorf("expected the global default (firefox) used, got firefox.calls=%d base.calls=%d", firefox.calls, base.calls)
	}
}

// TestRenderAwareFetcher_ExplicitNoneOverridesGlobalDefault proves a
// per-fetch "none" beats a non-none global default -- distinct from the
// empty-string "inherit" case above.
func TestRenderAwareFetcher_ExplicitNoneOverridesGlobalDefault(t *testing.T) {
	base := &fakeAuthFetcher{body: "plain html"}
	chromium := &fakeRenderer{}
	opValues := domain.DefaultOperationalSettings().Get()
	opValues.DefaultRenderer = domain.RendererChromium
	opSettings := domain.NewOperationalSettings(opValues)
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: opSettings,
		Renderers: map[string]ports.Renderer{domain.RendererChromium: chromium},
	}

	html, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{Renderer: domain.RendererNone})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "plain html" || base.calls != 1 || chromium.calls != 0 {
		t.Errorf("expected an explicit 'none' override to force the plain fetcher, got html=%q base.calls=%d chromium.calls=%d", html, base.calls, chromium.calls)
	}
}

// TestRenderAwareFetcher_NoRenderForcesPlainFetchEvenWithRendererSet proves
// NoRender (crawlLoop's sitemap.xml guard) wins over any Renderer value,
// including an explicit per-fetch one.
func TestRenderAwareFetcher_NoRenderForcesPlainFetchEvenWithRendererSet(t *testing.T) {
	base := &fakeAuthFetcher{body: "sitemap xml"}
	chromium := &fakeRenderer{}
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: domain.DefaultOperationalSettings(),
		Renderers: map[string]ports.Renderer{domain.RendererChromium: chromium},
	}

	html, err := f.FetchWithOptions(context.Background(), "http://a/sitemap.xml", ports.FetchOptions{Renderer: domain.RendererChromium, NoRender: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "sitemap xml" || base.calls != 1 || chromium.calls != 0 {
		t.Errorf("expected NoRender to force the plain fetcher despite Renderer set, got html=%q base.calls=%d chromium.calls=%d", html, base.calls, chromium.calls)
	}
}

func TestRenderAwareFetcher_UnavailableRendererIsAFetchError(t *testing.T) {
	base := &fakeAuthFetcher{}
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: domain.DefaultOperationalSettings(),
		Renderers: map[string]ports.Renderer{}, // chromium not registered -- e.g. never configured
	}

	_, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{Renderer: domain.RendererChromium})
	if err == nil {
		t.Fatal("expected an error for an unavailable renderer")
	}
	if base.calls != 0 {
		t.Error("expected no silent fallback to the plain fetcher")
	}
}

func TestRenderAwareFetcher_RendererErrorPropagates(t *testing.T) {
	base := &fakeAuthFetcher{}
	wantErr := errors.New("navigation timeout")
	chromium := &fakeRenderer{err: wantErr}
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: domain.DefaultOperationalSettings(),
		Renderers: map[string]ports.Renderer{domain.RendererChromium: chromium},
	}

	if _, err := f.FetchWithOptions(context.Background(), "http://a", ports.FetchOptions{Renderer: domain.RendererChromium}); !errors.Is(err, wantErr) {
		t.Errorf("expected the renderer's own error to propagate, got %v", err)
	}
}

func TestRenderAwareFetcher_FetchAlwaysUsesPlainPath(t *testing.T) {
	base := &fakeAuthFetcher{body: "robots txt"}
	chromium := &fakeRenderer{}
	opValues := domain.DefaultOperationalSettings().Get()
	opValues.DefaultRenderer = domain.RendererChromium
	opSettings := domain.NewOperationalSettings(opValues)
	f := &application.RenderAwareFetcher{
		Base: base, OpSettings: opSettings,
		Renderers: map[string]ports.Renderer{domain.RendererChromium: chromium},
	}

	// Fetch (no options) is what robots.New's checker calls -- must never
	// render, even with a non-none global default configured.
	html, err := f.Fetch(context.Background(), "http://a/robots.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if html != "robots txt" || base.calls != 1 || chromium.calls != 0 {
		t.Errorf("expected Fetch to always use the plain path, got html=%q base.calls=%d chromium.calls=%d", html, base.calls, chromium.calls)
	}
}
