package robots

import (
	"context"
	"net/url"
	"strings"
	"sync"
)

type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

type Checker struct {
	fetcher Fetcher
	mu      sync.Mutex
	cache   map[string][]string
}

func New(fetcher Fetcher) *Checker {
	return &Checker{fetcher: fetcher, cache: make(map[string][]string)}
}

func (c *Checker) Allowed(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	origin := u.Scheme + "://" + u.Host

	c.mu.Lock()
	disallows, cached := c.cache[origin]
	c.mu.Unlock()

	if !cached {
		disallows = c.loadRules(ctx, origin)
		c.mu.Lock()
		c.cache[origin] = disallows
		c.mu.Unlock()
	}

	for _, prefix := range disallows {
		if prefix != "" && strings.HasPrefix(u.Path, prefix) {
			return false
		}
	}
	return true
}

func (c *Checker) loadRules(ctx context.Context, origin string) []string {
	body, err := c.fetcher.Fetch(ctx, origin+"/robots.txt")
	if err != nil {
		return nil
	}

	var rules []string
	relevant := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "user-agent:"):
			agent := strings.TrimSpace(line[len("User-agent:"):])
			relevant = agent == "*"
		case relevant && strings.HasPrefix(low, "disallow:"):
			path := strings.TrimSpace(line[len("Disallow:"):])
			rules = append(rules, path)
		}
	}
	return rules
}
