CREATE TABLE IF NOT EXISTS memes (
    id           SERIAL PRIMARY KEY,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    country_code CHAR(2) NOT NULL,
    country_name TEXT NOT NULL,
    year         INT NOT NULL CHECK (year BETWEEN 1990 AND 2100),
    status       TEXT NOT NULL CHECK (status IN ('trend', 'classic', 'outdated')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS memes_country_idx ON memes (country_code);
CREATE INDEX IF NOT EXISTS memes_year_idx ON memes (year);
