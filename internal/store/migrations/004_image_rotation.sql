-- 004: persist image viewer rotation

ALTER TABLE files ADD COLUMN rotation INTEGER NOT NULL DEFAULT 0;