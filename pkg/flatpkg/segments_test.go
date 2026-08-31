package flatpkg

import "testing"

// TestSegmentSizes pins the rule measured against pkgbuild on macOS 26: a
// file is split only when an odc header cannot express its size, which is
// the same 8 GiB the --large-payload documentation calls a large file, and
// the pieces are 1 GiB each.
func TestSegmentSizes(t *testing.T) {
	const g = 1 << 30
	cases := []struct {
		name  string
		size  int64
		large bool
		want  []int64
	}{
		{"small file, not large-payload", 100, false, []int64{100}},
		{"small file, large-payload", 100, true, []int64{100}},
		// 5 GiB fits an odc header, and pkgbuild leaves it whole even
		// with --large-payload.
		{"5 GiB stays whole", 5 * g, true, []int64{5 * g}},
		{"the largest odc can express", odcMaxFileSize, true, []int64{odcMaxFileSize}},
		// One byte more must be split. That size is exactly 8 GiB, so it
		// divides into eight whole pieces with nothing left over.
		{"one byte over", odcMaxFileSize + 1, true, []int64{g, g, g, g, g, g, g, g}},
		// A size that does not divide evenly keeps the remainder last.
		{"remainder comes last", 9*g + 7, true, []int64{g, g, g, g, g, g, g, g, g, 7}},
		{"9 GiB in nine pieces", 9 * g, true, []int64{g, g, g, g, g, g, g, g, g}},
		// Without --large-payload nothing is split; the caller reports
		// the file as too large instead.
		{"over the limit without the flag", 9 * g, false, []int64{9 * g}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := segmentSizes(tc.size, tc.large)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d segments, want %d", len(got), len(tc.want))
			}
			var total int64
			for i, n := range got {
				if n != tc.want[i] {
					t.Errorf("segment %d = %d, want %d", i, n, tc.want[i])
				}
				total += n
			}
			if total != tc.size {
				t.Errorf("segments total %d, want %d", total, tc.size)
			}
		})
	}
}
