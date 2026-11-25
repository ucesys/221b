-- Knowledge at scale (phase-13.1-ai, part B; KN-53 to KN-58). The search index of
-- Mycroft's wiki in the database, shared by every replica, in place of the index
-- each replica built in memory from its copy of the wiki: a document is an approved
-- page or the excerpt of one source item (a long document's section, KN-52), by the
-- path of its file in the wiki; its passages are its chunks, each with its words for
-- PostgreSQL's full-text search. The leader keeps the tables as the wiki holds: a
-- document whose file changed is written again whole, one whose file is gone is
-- deleted. A passage's embedding is the chosen embedding model's, written by the
-- leader's embedding job; a change of model writes them again. The extensions the
-- search uses besides (pg_trgm, vector) are not made here: the console looks for
-- them and searches without what is missing, and the indexes that need pg_trgm are
-- made when it is there.
--
-- A source's index settings: its trust level (high, normal or low; empty is its
-- kind's default), the product and version its documents are of, and whether its
-- passages are kept to the versions the clusters run.

ALTER TABLE knowledge_sources ADD COLUMN trust text NOT NULL DEFAULT '';
ALTER TABLE knowledge_sources ADD COLUMN product text NOT NULL DEFAULT '';
ALTER TABLE knowledge_sources ADD COLUMN product_version text NOT NULL DEFAULT '';
ALTER TABLE knowledge_sources ADD COLUMN follows_cluster_versions boolean NOT NULL DEFAULT false;

CREATE TABLE knowledge_documents (
    knowledge_document_id uuid PRIMARY KEY,
    -- The file in the wiki: pages/… or excerpts/….
    path text NOT NULL UNIQUE,
    -- page or excerpt.
    kind text NOT NULL,
    -- An excerpt's source; none for a page. Its passages go with the source.
    knowledge_source_id text REFERENCES knowledge_sources (knowledge_source_id) ON DELETE CASCADE,
    -- An excerpt's item, as the manifest keys it ("source:id").
    item_key text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    -- A section's heading path ("Backups › Restore").
    heading text NOT NULL DEFAULT '',
    -- A page's kind (runbook, system…).
    page_kind text NOT NULL DEFAULT '',
    -- The item's version in its source.
    item_version text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    -- A page's last update, or the item's own date.
    dated_at timestamptz,
    -- What it is about, from the source's settings or the document's front matter or
    -- path: a product (rke2) and its version as major.minor (1.33).
    product text NOT NULL DEFAULT '',
    product_version text NOT NULL DEFAULT '',
    -- An approved page is reviewed; an excerpt is not.
    is_reviewed boolean NOT NULL DEFAULT false,
    -- The hash of the file and the tags it was indexed with.
    content_hash text NOT NULL,
    indexed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_documents_source_idx ON knowledge_documents (knowledge_source_id);
COMMENT ON TABLE knowledge_documents IS 'The wiki''s approved pages and source excerpts the knowledge search finds';

CREATE TABLE knowledge_passages (
    knowledge_passage_id uuid PRIMARY KEY,
    knowledge_document_id uuid NOT NULL REFERENCES knowledge_documents (knowledge_document_id) ON DELETE CASCADE,
    position integer NOT NULL,
    body text NOT NULL,
    -- The body's letters and digits alone, in lower case: r740a and r740-a meet.
    squashed text NOT NULL DEFAULT '',
    -- The document's title and heading (weight A) and the body (D), in English.
    search tsvector NOT NULL,
    UNIQUE (knowledge_document_id, position)
);
CREATE INDEX knowledge_passages_search_idx ON knowledge_passages USING gin (search);
COMMENT ON TABLE knowledge_passages IS 'The chunks of each indexed document, with their words for full-text search';

CREATE TABLE knowledge_embeddings (
    knowledge_passage_id uuid PRIMARY KEY REFERENCES knowledge_passages (knowledge_passage_id) ON DELETE CASCADE,
    -- The embedding model, as Administration › AI › Knowledge names it, and its size.
    model text NOT NULL,
    dimensions integer NOT NULL,
    -- Cast to pgvector's vector where the extension is there.
    embedding real[] NOT NULL,
    embedded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_embeddings_model_idx ON knowledge_embeddings (model);
COMMENT ON TABLE knowledge_embeddings IS 'Each passage''s embedding by the chosen embedding model, for semantic search';
