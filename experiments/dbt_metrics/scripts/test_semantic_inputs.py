import unittest
from collections import Counter
from datetime import datetime, timedelta, timezone

from semantic_inputs import align_reconstructed_segments, prepare_sleep


class ReconstructedBoundaryTests(unittest.TestCase):
    start = datetime(2026, 10, 1, 1, tzinfo=timezone.utc)

    def interval(self, start, end, stage='sleep_core', whole=True):
        return self.start + timedelta(seconds=start), self.start + timedelta(seconds=end), stage, whole

    def test_both_directions_and_inclusive_limit(self):
        for offset in (-1, -0.345678, 0.456789, 1):
            with self.subTest(offset=offset):
                stats = Counter()
                segments = [self.interval(0, 10 + offset), self.interval(10, 20, 'sleep_rem')]
                aligned = align_reconstructed_segments(segments, stats)
                self.assertEqual(aligned[0][1], aligned[1][0])
                self.assertEqual(stats, {'gaps_aligned' if offset < 0 else 'overlaps_aligned': 1})

    def test_larger_gaps_and_overlaps_preserved(self):
        for offset in (-1.000001, 1.000001):
            with self.subTest(offset=offset):
                segments = [self.interval(0, 10 + offset), self.interval(10, 20)]
                self.assertEqual(align_reconstructed_segments(segments, Counter()), [s[:3] for s in segments])

    def test_ambiguous_boundaries_preserved(self):
        cases = [
            [self.interval(0, 10.5), self.interval(10, 20), self.interval(11, 30)],
            [self.interval(0, 10.5), self.interval(10, 20), self.interval(10, 20, 'sleep_rem')],
            [self.interval(0, 10.5), self.interval(0, 10.5, 'sleep_rem'), self.interval(10, 20)],
            [self.interval(0, 10.5), self.interval(5, 7), self.interval(10, 20)],
        ]
        for segments in cases:
            with self.subTest(segments=segments):
                stats = Counter()
                self.assertEqual(align_reconstructed_segments(segments, stats), [s[:3] for s in segments])
                self.assertGreater(stats['ambiguous_unchanged'], 0)

    def test_degenerate_and_exact_duplicate(self):
        stats = Counter()
        left = self.interval(0, 10.5)
        next_stage = self.interval(10, 20)
        aligned = align_reconstructed_segments([left, next_stage, next_stage, self.interval(30, 30)], stats)
        self.assertEqual(len(aligned), 2)
        self.assertEqual(aligned[0][1], aligned[1][0])
        self.assertEqual(stats, {'overlaps_aligned': 1, 'degenerate_skipped': 1})

    def test_adapter_keeps_fractional_writers_and_other_sources_separate(self):
        for first, second, second_source in [('01:00:00.000', '01:00:10', 'a'),
                                             ('01:00:00', '01:00:10.123', 'a'),
                                             ('01:00:00', '01:00:10', 'b')]:
            with self.subTest(first=first, second=second, source=second_source):
                data = {'calendar': [('2026-10-01',)],
                        'completed_night_sleep': [('2026-10-01', 20 / 3600, 'a', '', '', '', 'complete', 'plausible')],
                        'segment': [(f'2026-10-01 {first} +0000', 'a', 'sleep_core', 10.5 / 3600),
                                    (f'2026-10-01 {second} +0000', second_source, 'sleep_deep', 10 / 3600)]}
                intervals, _, _, stats = prepare_sleep(data)
                self.assertEqual(intervals[0][3], self.start + timedelta(seconds=10.5))
                self.assertFalse(stats)


if __name__ == '__main__':
    unittest.main()
