-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM environments) THEN
  RAISE EXCEPTION 'This prerelease changes Kubernetes identities and requires a fresh installation. Export data before recreating the installation; existing data has not been changed.';
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
