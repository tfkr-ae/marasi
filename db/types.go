package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Metadata represents a flexible key-value store for additional data, stored as JSON in the database.
// It implements the sql.Scanner and driver.Valuer interfaces to handle database serialization.
type Metadata map[string]any

// Scan implements the sql.Scanner interface, allowing Metadata to be read from the database.
func (m *Metadata) Scan(value interface{}) error {
	if value == nil {
		*m = make(Metadata)
		return nil
	}

	switch v := value.(type) {
	case []byte:
		json.Unmarshal(v, &m)
		return nil
	case string:
		json.Unmarshal([]byte(v), &m)
		return nil
	default:
		return fmt.Errorf("unsupported type %T", v)
	}
}

// Value implements the driver.Valuer interface, allowing Metadata to be written to the database.
func (m Metadata) Value() (driver.Value, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	return json.Marshal(m)
}
