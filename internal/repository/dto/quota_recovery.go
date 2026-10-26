package dto

import "time"

type QuotaRecoveryObservation struct {
	QueryStartedAt    time.Time `json:"query_started_at"`
	QueryCompletedAt  time.Time `json:"query_completed_at"`
	RemainingFraction float64   `json:"remaining_fraction"`
	ResetAt           time.Time `json:"reset_at"`
}

type QuotaRecoveryTimeRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type QuotaRecoveryEvidence struct {
	AccountRef              string                   `json:"account_ref"`
	Plan                    string                   `json:"plan"`
	Before                  QuotaRecoveryObservation `json:"before"`
	After                   QuotaRecoveryObservation `json:"after"`
	RemainingIncreasePoints float64                  `json:"remaining_increase_points"`
	ResetDelaySeconds       int64                    `json:"reset_delay_seconds"`
	OccurredBetween         QuotaRecoveryTimeRange   `json:"occurred_between"`
}

type QuotaRecoveryEvent struct {
	ID                  string                  `json:"id"`
	Type                string                  `json:"type"`
	RuleVersion         int                     `json:"rule_version"`
	DetectorStartedAt   time.Time               `json:"detector_started_at"`
	FirstDetectedAt     time.Time               `json:"first_detected_at"`
	DetectedAt          time.Time               `json:"detected_at"`
	RecordedAt          time.Time               `json:"recorded_at"`
	AccountCount        int                     `json:"account_count"`
	KeeperResetExcluded bool                    `json:"keeper_reset_excluded"`
	ExternalResetStatus string                  `json:"external_reset_status"`
	Accounts            []QuotaRecoveryEvidence `json:"accounts"`
}

type QuotaRecoveryEventsPage struct {
	Events            []QuotaRecoveryEvent `json:"events"`
	NextAfterID       string               `json:"next_after_id"`
	HasMore           bool                 `json:"has_more"`
	DetectorStartedAt time.Time            `json:"detector_started_at"`
}
