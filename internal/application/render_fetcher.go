package application

import (
	"context"
	"fmt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// RenderAwareFetcher wraps a plain ports.AuthFetcher, dispatching a
// FetchWithOptions call to a real (headless) browser instead whenever a
// renderer is actually requested -- explicitly per-fetch (opts.Renderer)
// or, when that's empty, whatever DefaultRenderer the Tuning page
// currently has configured. opts.NoRender always forces the plain path
// regardless (crawlLoop sets it for its own sitemap.xml fetch, which must
// never go through a browser). Fetch (the no-options method robots.New's
// checker calls) also always takes the plain path -- robots.txt is plain
// text, never worth a real browser.
//
// Renderers is keyed by domain.Renderer* constant ("chromium"/"firefox");
// a name that isn't a key (an unavailable/misconfigured engine) is a
// fetch error for that URL rather than a silent fallback to plain HTTP --
// an admin who turned rendering on should see a clear failure, not
// unknowingly get unrendered content while believing otherwise. crawlLoop
// already treats one URL's fetch error as non-fatal to the whole crawl.
type RenderAwareFetcher struct {
	Base       ports.AuthFetcher
	Renderers  map[string]ports.Renderer
	OpSettings *domain.OperationalSettings
}

var _ ports.AuthFetcher = (*RenderAwareFetcher)(nil)

func (f *RenderAwareFetcher) Fetch(ctx context.Context, url string) (string, error) {
	return f.Base.FetchWithOptions(ctx, url, ports.FetchOptions{})
}

func (f *RenderAwareFetcher) FetchWithOptions(ctx context.Context, url string, opts ports.FetchOptions) (string, error) {
	name := f.rendererName(opts)
	if name == domain.RendererNone {
		return f.Base.FetchWithOptions(ctx, url, opts)
	}
	r, ok := f.Renderers[name]
	if !ok {
		return "", fmt.Errorf("renderer %q is not available on this crawl-server", name)
	}
	return r.Render(ctx, url, opts)
}

func (f *RenderAwareFetcher) rendererName(opts ports.FetchOptions) string {
	if opts.NoRender {
		return domain.RendererNone
	}
	name := opts.Renderer
	if name == domain.RendererDefault {
		name = f.OpSettings.Get().DefaultRenderer
	}
	if name == domain.RendererDefault {
		name = domain.RendererNone
	}
	return name
}
