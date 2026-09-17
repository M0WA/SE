package application

import (
	"context"
	"fmt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// RenderAwareFetcher wraps a plain ports.AuthFetcher, routing
// FetchWithOptions through a real (headless) browser when a renderer is
// requested (opts.Renderer, or DefaultRenderer if empty); opts.NoRender
// and the no-options Fetch always take the plain path. An unknown/
// unavailable Renderers name errors rather than silently falling back to
// plain HTTP, so a misconfigured admin setting fails loudly instead of
// quietly serving unrendered content.
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
