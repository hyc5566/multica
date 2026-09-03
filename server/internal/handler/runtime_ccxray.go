package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxCCXRaySummaryBytes      = 1024
	maxCCXRayVersionBytes      = 64
	maxCCXRayErrorCodeBytes    = 64
	maxCCXRayObservationAge    = 24 * time.Hour
	maxCCXRayObservationFuture = time.Minute
)

var ccxrayErrorCodeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]*$`)

type ccxrayHealthSummaryWire struct {
	Enabled       *bool   `json:"enabled"`
	Installed     *bool   `json:"installed"`
	Status        *string `json:"status"`
	Version       *string `json:"version"`
	ObservedAt    *string `json:"observed_at"`
	LastErrorCode *string `json:"last_error_code"`
}

func validateCCXRayHealthSummary(raw json.RawMessage, now time.Time) ([]byte, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if len(raw) > maxCCXRaySummaryBytes {
		return nil, fmt.Errorf("ccxray summary exceeds %d bytes", maxCCXRaySummaryBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire ccxrayHealthSummaryWire
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode ccxray summary: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("ccxray summary must contain one object")
	}
	if wire.Enabled == nil || wire.Installed == nil || wire.Status == nil ||
		wire.Version == nil || wire.ObservedAt == nil || wire.LastErrorCode == nil {
		return nil, errors.New("ccxray summary is missing a required field")
	}
	if len(*wire.Version) > maxCCXRayVersionBytes {
		return nil, errors.New("ccxray version is too long")
	}
	if len(*wire.LastErrorCode) > maxCCXRayErrorCodeBytes ||
		!ccxrayErrorCodeRE.MatchString(*wire.LastErrorCode) {
		return nil, errors.New("ccxray error code is invalid")
	}

	observedAt, err := time.Parse(time.RFC3339, *wire.ObservedAt)
	if err != nil {
		return nil, errors.New("ccxray observed_at must be RFC3339")
	}
	if observedAt.Before(now.Add(-maxCCXRayObservationAge)) ||
		observedAt.After(now.Add(maxCCXRayObservationFuture)) {
		return nil, errors.New("ccxray observed_at is outside the accepted window")
	}

	switch *wire.Status {
	case protocol.CCXRayStatusDisabled:
		if *wire.Enabled {
			return nil, errors.New("disabled ccxray summary cannot be enabled")
		}
	case protocol.CCXRayStatusNotInstalled:
		if !*wire.Enabled || *wire.Installed {
			return nil, errors.New("not_installed ccxray summary is inconsistent")
		}
	case protocol.CCXRayStatusObserving, protocol.CCXRayStatusDegraded:
		if !*wire.Enabled || !*wire.Installed {
			return nil, errors.New("active ccxray summary requires enabled and installed")
		}
	default:
		return nil, errors.New("ccxray status is invalid")
	}

	normalized, err := json.Marshal(protocol.CCXRayHealthSummary{
		Enabled:       *wire.Enabled,
		Installed:     *wire.Installed,
		Status:        *wire.Status,
		Version:       *wire.Version,
		ObservedAt:    observedAt.UTC().Format(time.RFC3339),
		LastErrorCode: *wire.LastErrorCode,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal ccxray summary: %w", err)
	}
	return normalized, nil
}

func (h *Handler) storeCCXRayHealthSummary(
	ctx context.Context,
	runtimeID pgtype.UUID,
	raw json.RawMessage,
) error {
	summary, err := validateCCXRayHealthSummary(raw, time.Now())
	if err != nil || summary == nil {
		return err
	}
	return h.Queries.UpdateAgentRuntimeCCXRaySummary(
		ctx,
		db.UpdateAgentRuntimeCCXRaySummaryParams{ID: runtimeID, Summary: summary},
	)
}
