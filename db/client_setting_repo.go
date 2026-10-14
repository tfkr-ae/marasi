package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tfkr-ae/marasi/domain"
)

type clientSettingRow struct {
	Client    string    `db:"client"`
	Key       string    `db:"key"`
	Value     string    `db:"value"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (row clientSettingRow) setting() domain.ClientSetting {
	return domain.ClientSetting{
		Client:    row.Client,
		Key:       row.Key,
		Value:     json.RawMessage(row.Value),
		UpdatedAt: row.UpdatedAt,
	}
}

// ListClientSettings implements the domain.ConfigRepository interface.
// It returns client's settings whose key starts with prefix, sorted by key.
func (repo *Repository) ListClientSettings(client, prefix string) ([]domain.ClientSetting, error) {
	if err := domain.ValidateClientName(client); err != nil {
		return nil, err
	}
	var rows []clientSettingRow
	err := repo.dbConn.Select(&rows, `
		SELECT client, key, value, updated_at FROM client_setting
		WHERE client = ? AND substr(key, 1, length(?)) = ?
		ORDER BY key`, client, prefix, prefix)
	if err != nil {
		return nil, fmt.Errorf("listing client settings %s: %w", client, err)
	}
	settings := make([]domain.ClientSetting, 0, len(rows))
	for _, row := range rows {
		settings = append(settings, row.setting())
	}
	return settings, nil
}

// DeleteClientSetting implements the domain.ConfigRepository interface.
// Deleting a missing key is not an error.
func (repo *Repository) DeleteClientSetting(client, key string) error {
	if err := domain.ValidateClientSettingAddress(client, key); err != nil {
		return err
	}
	if _, err := repo.dbConn.Exec(`DELETE FROM client_setting WHERE client = ? AND key = ?`, client, key); err != nil {
		return fmt.Errorf("deleting client setting %s/%s: %w", client, key, err)
	}
	return nil
}

// GetClientSetting implements the domain.ConfigRepository interface.
func (repo *Repository) GetClientSetting(client, key string) (domain.ClientSetting, error) {
	if err := domain.ValidateClientSettingAddress(client, key); err != nil {
		return domain.ClientSetting{}, err
	}
	var row clientSettingRow
	err := repo.dbConn.Get(&row, `SELECT client, key, value, updated_at FROM client_setting WHERE client = ? AND key = ?`, client, key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClientSetting{}, domain.ErrClientSettingNotFound
	}
	if err != nil {
		return domain.ClientSetting{}, fmt.Errorf("getting client setting %s/%s: %w", client, key, err)
	}
	return row.setting(), nil
}

// SetClientSetting implements the domain.ConfigRepository interface.
// It upserts the single row for client and key, and refuses domain.ReservedClient.
func (repo *Repository) SetClientSetting(client, key string, value json.RawMessage) (domain.ClientSetting, error) {
	if err := domain.ValidateClientSettingAddress(client, key); err != nil {
		return domain.ClientSetting{}, err
	}
	if client == domain.ReservedClient {
		return domain.ClientSetting{}, fmt.Errorf("%w: %s", domain.ErrReservedClient, client)
	}
	if err := domain.ValidateClientSettingValue(value); err != nil {
		return domain.ClientSetting{}, err
	}
	row := clientSettingRow{Client: client, Key: key, Value: string(value), UpdatedAt: time.Now().UTC()}
	_, err := repo.dbConn.NamedExec(`
		INSERT INTO client_setting (client, key, value, updated_at)
		VALUES (:client, :key, :value, :updated_at)
		ON CONFLICT (client, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, row)
	if err != nil {
		return domain.ClientSetting{}, fmt.Errorf("setting client setting %s/%s: %w", client, key, err)
	}
	return row.setting(), nil
}
