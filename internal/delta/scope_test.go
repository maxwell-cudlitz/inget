package delta

import (
	"testing"
)

func TestMatchingFragments(t *testing.T) {
	keys := []string{
		"src/main.go",
		"src/util/helper.go",
		"docs/readme.md",
		"config.yaml",
		"src/deep/nested/file.go",
	}

	tests := []struct {
		name      string
		dependsOn []string
		want      []string
	}{
		{
			name:      "doublestar matches everything",
			dependsOn: []string{"**"},
			want:      keys,
		},
		{
			name:      "single segment glob",
			dependsOn: []string{"*.yaml"},
			want:      []string{"config.yaml"},
		},
		{
			name:      "directory glob",
			dependsOn: []string{"src/**"},
			want:      []string{"src/main.go", "src/util/helper.go", "src/deep/nested/file.go"},
		},
		{
			name:      "multi-segment path pattern",
			dependsOn: []string{"src/util/*.go"},
			want:      []string{"src/util/helper.go"},
		},
		{
			name:      "no matches",
			dependsOn: []string{"*.rs"},
			want:      nil,
		},
		{
			name:      "multiple patterns",
			dependsOn: []string{"docs/**", "*.yaml"},
			want:      []string{"docs/readme.md", "config.yaml"},
		},
		{
			name:      "empty depends on",
			dependsOn: []string{},
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchingFragments(keys, tt.dependsOn)
			if !sliceEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestViewSkippable(t *testing.T) {
	allKeys := []string{"src/a.go", "src/b.go", "docs/readme.md", "config.yaml"}

	tests := []struct {
		name        string
		changedKeys []string
		dependsOn   []string
		want        bool
	}{
		{
			name:        "no changed keys match deps",
			changedKeys: []string{"docs/readme.md"},
			dependsOn:   []string{"src/**"},
			want:        true,
		},
		{
			name:        "changed key matches deps",
			changedKeys: []string{"src/a.go"},
			dependsOn:   []string{"src/**"},
			want:        false,
		},
		{
			name:        "no changes at all",
			changedKeys: []string{},
			dependsOn:   []string{"**"},
			want:        true,
		},
		{
			name:        "all changed with broad deps",
			changedKeys: allKeys,
			dependsOn:   []string{"**"},
			want:        false,
		},
		{
			// A deleted fragment is changed but no longer present in the item, so its key
			// must still invalidate the views that depended on it.
			name:        "deleted key absent from the current fragment set",
			changedKeys: []string{"src/removed.go"},
			dependsOn:   []string{"src/**"},
			want:        false,
		},
		{
			name:        "deleted key outside the globs",
			changedKeys: []string{"vendor/removed.go"},
			dependsOn:   []string{"src/**"},
			want:        true,
		},
		{
			name:        "empty depends_on means nothing matches",
			changedKeys: allKeys,
			dependsOn:   []string{},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ViewSkippable(tt.changedKeys, tt.dependsOn)
			if got != tt.want {
				t.Errorf("ViewSkippable = %v, want %v", got, tt.want)
			}
		})
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
