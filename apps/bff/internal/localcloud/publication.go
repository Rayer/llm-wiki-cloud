package localcloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PublicationReceipt is a create-only post-commit record. The committed
// manifest also carries the local execution ID so BFF can recover when this
// optional receipt write fails.
type PublicationReceipt struct {
	ExecutionID        string `json:"execution_id"`
	GenerationID       string `json:"generation_id"`
	ManifestGeneration int64  `json:"manifest_generation"`
}

func (r PublicationReceipt) Validate() error {
	if strings.TrimSpace(r.ExecutionID) == "" || strings.TrimSpace(r.GenerationID) == "" || r.ManifestGeneration <= 0 {
		return errors.New("invalid local publication receipt")
	}
	if _, err := Parse(r.ExecutionID); err != nil {
		return fmt.Errorf("invalid local publication receipt execution ID: %w", err)
	}
	return nil
}

func EncodePublicationReceipt(receipt PublicationReceipt) ([]byte, error) {
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func DecodePublicationReceipt(data []byte) (PublicationReceipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt PublicationReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return PublicationReceipt{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PublicationReceipt{}, errors.New("invalid trailing local publication receipt data")
	}
	if err := receipt.Validate(); err != nil {
		return PublicationReceipt{}, err
	}
	return receipt, nil
}

func PublicationReceiptPath(executionID string) (string, error) {
	if _, err := Parse(executionID); err != nil {
		return "", err
	}
	return "cache/local-pipeline-" + executionID + ".commit.json", nil
}
