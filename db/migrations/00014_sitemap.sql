-- +goose Up
-- The stored sitemap (ADR-0040). Each node is an origin or one path segment
-- below it, named by its canonical path. sitemap_count holds subtree totals
-- per content type; the '' bucket holds pairs with no content type, including
-- in-flight ones. pair_sitemap links each pair to its leaf node. The sitemap
-- background job fills in pairs that have no link.
CREATE TABLE IF NOT EXISTS sitemap_node (
    id INTEGER PRIMARY KEY,
    parent_id INTEGER REFERENCES sitemap_node(id),
    name TEXT NOT NULL UNIQUE,
    depth INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sitemap_node_parent ON sitemap_node(parent_id);

CREATE TABLE IF NOT EXISTS sitemap_count (
    node_id INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    pairs INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, content_type)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS pair_sitemap (
    request_id TEXT PRIMARY KEY REFERENCES request(id) ON DELETE CASCADE,
    node_id INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS pair_sitemap_node ON pair_sitemap(node_id);

-- +goose Down
DROP TABLE IF EXISTS pair_sitemap;
DROP TABLE IF EXISTS sitemap_count;
DROP TABLE IF EXISTS sitemap_node;
