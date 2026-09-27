package casepack

import (
	_ "embed"
	"encoding/json"

	"edge-decode-station/internal/domain"
)

//go:embed edge-decode-station.case.json
var embedded []byte

type Pack struct {
	Schema      string                    `json:"schema"`
	Kind        string                    `json:"kind"`
	Version     int                       `json:"version"`
	Description string                    `json:"description"`
	Project     domain.Project            `json:"project"`
	Events      []domain.Event            `json:"events"`
	ExpectedCodes []string                 `json:"expected_codes"`
}

func Load() (Pack, error) {
	var pack Pack
	if err := json.Unmarshal(embedded, &pack); err != nil { return Pack{}, err }
	return pack, nil
}

func Bytes() []byte { return embedded }
