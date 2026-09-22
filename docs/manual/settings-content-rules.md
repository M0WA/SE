# Settings: Content rules

[← Manual home](README.md)

*`/admin/settings`*

The "Content rules" group on the [Settings](settings.md) page.

## Blocked: Blocked words

One word per line; any document whose title or body contains one of these words is excluded from search results entirely, not just down-ranked. Matching uses the same tokenization as search itself, so capitalization and punctuation don't matter — entering "Spam" and "spam" behave identically. Use this for genuinely disqualifying content (profanity, known spam markers) rather than topics you merely want to rank lower, which is what boosted terms with a factor below 1 are for.

## Blocked: Blocked domains

One host or URL per line; any document served from one of these is excluded from results entirely, the domain-level counterpart to blocked words. Use the bare host (e.g. spammy.example) — matching is against the document's URL host. This is the right tool for cutting off an entire low-quality or spam source rather than trying to enumerate every objectionable word it might contain.

## Boosted: Boosted words

One "word factor" pair per line (e.g. "official 1.5") — a document whose title or body matches the word has its final score multiplied by that factor. A factor above 1 pushes matching documents higher; a factor between 0 and 1 pushes them lower without excluding them outright, which is the main alternative to fully blocking a word. If multiple boosted words match the same document, their factors multiply together, so stacking several modest boosts can compound into a large effect — keep that in mind before adding many overlapping boost terms. A zero or blank factor is simply ignored.

## Boosted: Boosted domains

One "host factor" pair per line (e.g. "trusted.example 2.0"), following the exact same rules as boosted words but matched against the document's URL host instead of its text. Use this to consistently favor documents from sources you know are authoritative for your use case, without needing to also enumerate the specific words that make them good.

---
← [Settings: Crawling](settings-crawling.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: System](settings-system.md) →
