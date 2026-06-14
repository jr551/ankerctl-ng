package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type HistoryAIResult struct {
	At                  time.Time      `json:"at"`
	Manual              bool           `json:"manual,omitempty"`
	ProviderURL         string         `json:"provider_url,omitempty"`
	Model               string         `json:"model,omitempty"`
	Prompt              string         `json:"prompt,omitempty"`
	FrameCount          int            `json:"frame_count,omitempty"`
	FrameSpacingSec     int            `json:"frame_spacing_sec,omitempty"`
	ReferenceImage      bool           `json:"reference_image,omitempty"`
	ModelFailing        bool           `json:"model_failing,omitempty"`
	Failing             bool           `json:"failing"`
	ThresholdPassed     bool           `json:"threshold_passed,omitempty"`
	Confidence          float64        `json:"confidence,omitempty"`
	ConfidenceThreshold float64        `json:"confidence_threshold,omitempty"`
	Reason              string         `json:"reason,omitempty"`
	Error               string         `json:"error,omitempty"`
	HTTPStatus          int            `json:"http_status,omitempty"`
	RawResponse         string         `json:"raw_response,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	EvidenceRelpath     string         `json:"evidence_relpath,omitempty"`
	EvidenceExpiresAt   *time.Time     `json:"evidence_expires_at,omitempty"`
}

type HistoryNotificationResult struct {
	At          time.Time `json:"at"`
	Event       string    `json:"event,omitempty"`
	OK          bool      `json:"ok"`
	Message     string    `json:"message,omitempty"`
	Transport   string    `json:"transport,omitempty"`
	Target      string    `json:"target,omitempty"`
	StatusCode  int       `json:"status_code,omitempty"`
	Title       string    `json:"title,omitempty"`
	ResponseRaw string    `json:"response_raw,omitempty"`
}

func (d *DB) AppendAIResult(filename, taskID string, result HistoryAIResult) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		rowID, history, err := loadHistoryAI(tx, filename, taskID)
		if err != nil || rowID == 0 {
			return err
		}
		history = append(history, result)
		data, err := json.Marshal(history)
		if err != nil {
			return fmt.Errorf("marshal ai history: %w", err)
		}
		_, err = tx.Exec(`UPDATE print_history SET ai_history_json=? WHERE id=?`, string(data), rowID)
		return err
	})
}

func (d *DB) AppendNotificationResult(filename, taskID string, result HistoryNotificationResult) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		rowID, history, err := loadHistoryNotifications(tx, filename, taskID)
		if err != nil || rowID == 0 {
			return err
		}
		history = append(history, result)
		data, err := json.Marshal(history)
		if err != nil {
			return fmt.Errorf("marshal notification history: %w", err)
		}
		_, err = tx.Exec(`UPDATE print_history SET notification_history_json=? WHERE id=?`, string(data), rowID)
		return err
	})
}

func decodeAIHistory(raw string) []HistoryAIResult {
	if raw == "" {
		return nil
	}
	var out []HistoryAIResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func decodeNotificationHistory(raw string) []HistoryNotificationResult {
	if raw == "" {
		return nil
	}
	var out []HistoryNotificationResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func loadHistoryAI(tx *sql.Tx, filename, taskID string) (int64, []HistoryAIResult, error) {
	rowID, raw, err := loadHistoryJSONColumn(tx, filename, taskID, "ai_history_json")
	if err != nil || rowID == 0 {
		return rowID, nil, err
	}
	return rowID, decodeAIHistory(raw), nil
}

func loadHistoryNotifications(tx *sql.Tx, filename, taskID string) (int64, []HistoryNotificationResult, error) {
	rowID, raw, err := loadHistoryJSONColumn(tx, filename, taskID, "notification_history_json")
	if err != nil || rowID == 0 {
		return rowID, nil, err
	}
	return rowID, decodeNotificationHistory(raw), nil
}

func loadHistoryJSONColumn(tx *sql.Tx, filename, taskID, column string) (int64, string, error) {
	rowID, err := findHistoryRowID(tx, filename, taskID)
	if err != nil || rowID == 0 {
		return rowID, "", err
	}
	var raw sql.NullString
	query := fmt.Sprintf("SELECT %s FROM print_history WHERE id=?", column)
	if err := tx.QueryRow(query, rowID).Scan(&raw); err != nil {
		return 0, "", fmt.Errorf("load %s: %w", column, err)
	}
	return rowID, raw.String, nil
}

func findHistoryRowID(tx *sql.Tx, filename, taskID string) (int64, error) {
	var rowID int64
	if taskID != "" {
		err := tx.QueryRow(`SELECT id FROM print_history WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID).Scan(&rowID)
		if err == nil {
			return rowID, nil
		}
		if err != sql.ErrNoRows {
			return 0, fmt.Errorf("find history row by task_id: %w", err)
		}
	}
	if filename != "" {
		err := tx.QueryRow(`SELECT id FROM print_history WHERE filename=? ORDER BY id DESC LIMIT 1`, filename).Scan(&rowID)
		if err == nil {
			return rowID, nil
		}
		if err != sql.ErrNoRows {
			return 0, fmt.Errorf("find history row by filename: %w", err)
		}
	}
	err := tx.QueryRow(`SELECT id FROM print_history ORDER BY id DESC LIMIT 1`).Scan(&rowID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find latest history row: %w", err)
	}
	return rowID, nil
}
