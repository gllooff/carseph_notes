-- 005: allow Markdown file uploads (kind 'markdown')

ALTER TABLE files RENAME TO files_old;

CREATE TABLE files (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id     TEXT REFERENCES folders(id) ON DELETE SET NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('image','pdf','markdown')),
    original_name TEXT NOT NULL,
    mime          TEXT NOT NULL,
    size          INTEGER NOT NULL,
    sha256        TEXT NOT NULL,
    filename      TEXT NOT NULL,          -- on-disk name: {id}.{ext}
    created_at    INTEGER NOT NULL,
    rotation      INTEGER NOT NULL DEFAULT 0
);

INSERT INTO files (id, user_id, folder_id, kind, original_name, mime, size, sha256, filename, created_at, rotation)
    SELECT id, user_id, folder_id, kind, original_name, mime, size, sha256, filename, created_at, rotation FROM files_old;

DROP TABLE files_old;

CREATE INDEX idx_files_user ON files (user_id, created_at DESC);
CREATE INDEX idx_files_folder ON files (folder_id);