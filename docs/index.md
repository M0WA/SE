# searchengine docs

A self-hosted hybrid (BM25 + semantic) search engine, written in Go. This
site is the same documentation as the repo's own [README](https://github.com/M0WA/SE)
and `docs/` folder, rendered as a real page instead of raw markdown.

## Start here

- **Using the admin UI?** [User manual](manual/) -- every admin settings
  page, screenshotted and explained field by field, plus the public
  search/chat page.
- **Installing or configuring a deployment?** [Configuration reference](configuration.md) --
  every environment variable, every config file under `packaging/`, every
  database-backed setting, and the full install checklist.
- **Understanding how it's built?** [Architecture](architecture/) -- the
  hexagonal-architecture layout, all seven binaries, and how they connect
  to external systems, with a diagram.

Source, issues, and every `packaging/*/README.md` referenced above live in
the repo itself: [github.com/M0WA/SE](https://github.com/M0WA/SE).
