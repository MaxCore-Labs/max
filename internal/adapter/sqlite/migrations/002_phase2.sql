-- +goose Up
ALTER TABLE runs ADD COLUMN config_rev TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE runs DROP COLUMN config_rev;
