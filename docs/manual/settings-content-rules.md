# Settings: Content rules

[← Manual home](README.md)

*`/admin/settings`*

The "Content rules" group on the [Settings](settings.md) page.

## Blocked: Blocked words

One word per line; any document whose title or body contains one is excluded from results entirely, not just down-ranked. Matching uses the same tokenization as search, so capitalization/punctuation don't matter. Use this for genuinely disqualifying content (profanity, spam markers), not topics you merely want ranked lower — that's what boosted terms with a factor below 1 are for.

## Blocked: Blocked domains

One host or URL per line; any document served from one is excluded entirely — the domain-level counterpart to blocked words. Use the bare host (e.g. spammy.example), matched against the document's URL host. Use this to cut off a whole low-quality source rather than enumerating every objectionable word it contains.

## Boosted: Boosted words

One "word factor" pair per line (e.g. "official 1.5") — a matching document's final score is multiplied by that factor. Above 1 pushes it higher; between 0 and 1 pushes it lower without excluding it, the main alternative to fully blocking a word. Multiple matching boosts multiply together, so several modest ones can compound into a large effect. A zero or blank factor is ignored.

## Boosted: Boosted domains

One "host factor" pair per line (e.g. "trusted.example 2.0"), same rules as boosted words but matched against URL host instead of text. Use this to favor sources you know are authoritative, without enumerating the specific words that make them good.

---
← [Settings: Crawling](settings-crawling.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: System](settings-system.md) →
