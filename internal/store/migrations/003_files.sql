-- 003: files (images & PDFs)

CREATE TABLE files (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id     TEXT REFERENCES folders(id) ON DELETE SET NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('image','pdf')),
    original_name TEXT NOT NULL,
    mime          TEXT NOT NULL,
    size          INTEGER NOT NULL,
    sha256        TEXT NOT NULL,
    filename      TEXT NOT NULL,          -- on-disk name: {id}.{ext}
    created_at    INTEGER NOT NULL
);

CREATE INDEX idx_files_user ON files (user_id, created_at DESC);
CREATE INDEX idx_files_folder ON files (folder_id);
