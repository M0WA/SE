# Crawl

[← Manual home](README.md)

*`/admin/crawl`*

This is where you start a new crawl of a site, either as a single one-off run or as a repeating schedule. Every option here — rendering, link scope, credentials, fetch overrides — is per-crawl: it only ever affects this one crawl, and leaving a field blank falls back to whatever the Settings page has configured site-wide.

![Crawl](images/crawl.png)

## URL and max pages

Type the site's URL into the main field and hit Crawl (or press Enter). You don't need to get the scheme or trailing slash exactly right — the field normalizes what you type when you tab or click away, adding https:// and stripping stray whitespace. Max pages caps how many pages this single crawl will fetch before it stops on its own, defaulting to 20,000; lower it for a quick spot-check of a site, or raise it if you know the site is larger than that and you want a complete pass.

## Scheduling: interval and max runs

Leave Interval at 0 (or blank) for a one-off crawl that runs once and is done — the submit button reads "Crawl" in that case. Enter a positive number of minutes and the button relabels itself to "Schedule": the crawl repeats on that interval indefinitely until you pause or delete it from the Schedules table on the Jobs page. Max runs only matters for a repeating crawl — leave it blank for unlimited repeats, or set it to have the schedule disable itself automatically after that many runs (for example, a one-time nightly crawl you want to stop after 30 nights).

## Rendering

"Use site default" follows whatever the Settings/Tuning page has configured. Most sites need nothing more than a plain HTTP fetch (None), which is fast and doesn't need anything extra installed. Pick Chromium or Firefox only for a site whose content is built by client-side JavaScript and wouldn't show up in a plain HTML fetch — it's substantially slower per page, and the first time either browser engine is used on this server it has to download it, so expect a delay on that first render-mode crawl.

## Respect robots.txt

Off by default for this form, meaning the crawl ignores robots.txt entirely unless you check this box. Turn it on when crawling a third-party site you don't control and want to behave like a polite crawler; leave it off for your own sites or internal targets where you already know it's fine to fetch everything.

## How far to follow links

Controls which discovered links this crawl will follow, from strictest to loosest: Exact seed host only stays on the literal host you seeded (e.g. only www.example.com); Seed's domain, including subdomains also follows blog.example.com from a seed of www.example.com, but not example.org; Same domain, any subdomain, any top-level domain is broader again — it follows example.org and www.example.de from a seed of example.com, but not other.com; Any domain follows everything discovered, anywhere. "Use site default" defers to the Settings page's global choice, which starts at the TLD-scope option out of the box since most real sites span more than one subdomain or TLD.

## Allowed / blocked domains

These two lists (one domain per line) sit on top of the link-scope choice above rather than replacing it. Allowed domains are always followed even if link scope would otherwise reject them — use this to pull in a specific partner or CDN domain without loosening scope for everything else. Blocked domains always win over both link scope and the allowed list — use this to keep a known-noisy or irrelevant domain out of the crawl even if scope would otherwise include it.

## Follow domains already in the index

Off by default. When checked, a discovered link is followed even outside the configured link scope as long as its domain already has at least one page indexed on this instance — the idea being that a domain you've already decided is worth indexing is worth following further, wherever it's linked from. Blocked domains still take precedence over this.

## Discover pages via sitemap.xml

Off by default. Check it to also fetch /sitemap.xml from the seed's domain and queue every URL it lists, in addition to whatever's found by following links from the seed page. Turn this on for a site with a sitemap you trust to be complete — it's a reliable way to make sure pages with no inbound links from the seed still get crawled.

## Prioritize pages not yet indexed

On by default. When checked, newly discovered pages are fetched ahead of pages already in the index, so a limited page budget (Max pages) goes toward new content first on a site that's mostly already indexed. Already-indexed pages still get crawled — just after every new page has had a chance to run. Turn it off if you specifically want to refresh already-indexed content before discovering anything new, e.g. after a site-wide content change.

## User-Agent, cookie, and Basic auth

These apply only to this one crawl — nothing here is saved anywhere, unlike the same fields on a schedule (see the schedule-detail page). Leave User-Agent blank to use the standard Firefox string the server sends by default, or set a custom one if a site blocks or misbehaves for common crawler user-agents. Cookie header lets you crawl content that sits behind a login by pasting a session cookie (e.g. session=abc123) captured from a real browser session. The Basic auth fields start read-only and become editable the moment you click into them — that's deliberate, so your browser doesn't try to autofill a saved password into what's actually a one-time crawl credential.

## Fetch overrides

Fetch timeout, Minimum text length, Delay between fetches, and Max response size each override the corresponding site-wide crawl default from the Settings page, for this crawl only. Leave any of them blank to use whatever's currently configured there — the placeholder text in each field shows you the actual current global value, not just a generic hint, so you can see exactly what you'd be overriding. Raise the delay if you're crawling a site you don't want to hit too hard; lower the minimum text length if you're deliberately indexing short pages (e.g. a documentation site with many short reference pages) that the global thin-content floor would otherwise skip; raise max response size if the site serves unusually large pages that are getting cut off.

## What happens after you submit

Submitting always creates the same kind of record — a crawl definition — whether or not you set an interval. A one-off crawl is due immediately; crawl-server's scheduler picks it up within a few seconds and creates the Job you'll see appear on the Jobs page. A repeating crawl is scheduled the same way but shows up under Schedules on the Jobs page instead, where you can pause it, edit its options, or delete it later.

> **Worth knowing:**
> - If you submit a URL whose domain already has a schedule or a pending one-off crawl for the same host, the new submission replaces that existing entry's options in place rather than creating a duplicate — there's at most one schedule per domain.
> - To change a crawl's options after creating it, or to see one you already scheduled, go to the Jobs page and click the edit icon on its row — that opens the schedule-detail page, which has the same fields plus a few schedule-only ones (Enabled, stored credentials).

---
← [Content Dedup](content-dedup.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Schedule detail](schedule-detail.md) →
