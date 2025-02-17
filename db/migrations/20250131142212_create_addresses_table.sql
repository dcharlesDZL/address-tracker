-- +goose Up
-- +goose StatementBegin
CREATE TABLE if not exists addresses(
    address text,
    chain text NOT NULL,
    nickname text NOT NULL,
    group_id integer NOT NULL,
    primary key (address, group_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS addresses;
-- +goose StatementEnd
