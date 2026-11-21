package sleepstudy

import "time"

type Stage struct {
	Kind string
	Span
}

// StagedCoverage separates confirmed asleep intervals from coarse-only edges
// that would extend an otherwise staged episode. The threshold is a shadow
// policy parameter. A wholly coarse source remains a valid duration source.
func StagedCoverage(stages []Stage, maxUncorroboratedEdge time.Duration) (asleep []Span, ambiguous bool) {
	var detailed, coarse []Span
	for _, stage := range stages {
		switch stage.Kind {
		case "deep", "rem", "core":
			detailed = append(detailed, stage.Span)
		case "asleep_unspecified":
			coarse = append(coarse, stage.Span)
		}
	}
	detailed = Union(detailed)
	asleep = Union(append(detailed[:len(detailed):len(detailed)], coarse...))
	if len(detailed) == 0 {
		return asleep, false
	}
	stagedEnvelope := Span{Start: detailed[0].Start, End: detailed[len(detailed)-1].End}
	coarseEdges := Duration(coarse) - Overlap(coarse, stagedEnvelope)
	return asleep, coarseEdges > maxUncorroboratedEdge
}

// AccountedDuration combines confirmed episode intervals in exactly one
// accounting window, regardless of how many devices observed them.
func AccountedDuration(episodes [][]Span, window Span) time.Duration {
	var candidates []Span
	for _, episode := range episodes {
		candidates = append(candidates, episode...)
	}
	return Overlap(candidates, window)
}
