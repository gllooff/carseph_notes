-- 002: folders, notes, tags

CREATE TABLE folders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL COLLATE NOCASE,
    created_at INTEGER NOT NULL,
    UNIQUE (user_id, name)
);

CREATE TABLE notes (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id  TEXT REFERENCES folders(id) ON DELETE SET NULL,
    title      TEXT NOT NULL DEFAULT 'Untitled',
    size       INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE tags (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT NOT NULL COLLATE NOCASE,
    UNIQUE (user_id, name)
);

CREATE TABLE item_tags (
    item_type TEXT NOT NULL CHECK (item_type IN ('note','file')),
    item_id   TEXT NOT NULL,
    tag_id    TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL,
    PRIMARY KEY (item_type, item_id, tag_id)
);

CREATE INDEX idx_notes_user_updated ON notes (user_id, updated_at DESC);
CREATE INDEX idx_notes_folder ON notes (folder_id);
CREATE INDEX idx_item_tags_tag ON item_tags (tag_id);
