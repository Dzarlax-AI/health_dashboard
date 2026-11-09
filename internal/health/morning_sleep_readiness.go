package health

import "time"

const (
	MorningSleepMissing             = "missing"
	MorningSleepCollecting          = "collecting"
	MorningSleepObservedStable      = "observed_stable"
	MorningSleepGappedOrConflicting = "gapped_or_conflicting"
)

// MorningSleepReadiness reports an observed episode, never a completed-night
// claim or permission to use sleep baselines and quality judgments.
type MorningSleepReadiness struct {
	Episode *MorningSleepEpisode `json:"episode,omitempty"`
	State   string               `json:"state,omitempty"`
	Reason  string               `json:"reason,omitempty"`
	Hours   float64              `json:"hours,omitempty"`
	Wake    time.Time            `json:"wake,omitempty"`
}

// MorningSleepEpisode contains one source-consistent recorded interval. These
// are observations available now, independent of later calendar finalization.
type MorningSleepEpisode struct {
	StageStatus         string     `json:"stage_status,omitempty"`
	UnclassifiedMinutes *int       `json:"unclassified_minutes,omitempty"`
	Start               *time.Time `json:"start,omitempty"`
	Wake                *time.Time `json:"wake,omitempty"`
	AsleepMinutes       int        `json:"asleep_minutes"`
	WindowMinutes       int        `json:"window_minutes,omitempty"`
	DeepMinutes         *int       `json:"deep_minutes,omitempty"`
	REMMinutes          *int       `json:"rem_minutes,omitempty"`
	CoreMinutes         *int       `json:"core_minutes,omitempty"`
	UnspecifiedMinutes  *int       `json:"unspecified_minutes,omitempty"`
	AwakeMinutes        *int       `json:"recorded_awake_minutes,omitempty"`
	AwakeBouts          *int       `json:"recorded_awake_bouts,omitempty"`
}
