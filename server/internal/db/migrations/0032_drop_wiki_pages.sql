-- The manifest of wiki pages is kept beside the pages, in Git or S3, and nothing reads or
-- writes this table: a page change is keyed by its run (step 0030). The owner decided on
-- November 19, 2026 to drop the model's tables that nothing uses.
DROP TABLE wiki_pages;
