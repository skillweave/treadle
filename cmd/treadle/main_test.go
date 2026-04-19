package main

import (
	"reflect"
	"testing"
)

func TestReorderFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty",
			in:   []string{},
			want: []string{},
		},
		{
			name: "all positional",
			in:   []string{"a", "b", "c"},
			want: []string{"a", "b", "c"},
		},
		{
			name: "all flags",
			in:   []string{"--foo=1", "--bar=2"},
			want: []string{"--foo=1", "--bar=2"},
		},
		{
			name: "flag first",
			in:   []string{"--foo=1", "a", "b"},
			want: []string{"--foo=1", "a", "b"},
		},
		{
			name: "flag last (the bug case)",
			in:   []string{"a", "b", "--foo=1"},
			want: []string{"--foo=1", "a", "b"},
		},
		{
			name: "flag in middle",
			in:   []string{"a", "--foo=1", "b"},
			want: []string{"--foo=1", "a", "b"},
		},
		{
			name: "multiple flags mixed with positionals",
			in:   []string{"a", "--foo=1", "b", "--bar=2", "c"},
			want: []string{"--foo=1", "--bar=2", "a", "b", "c"},
		},
		{
			name: "single dash counted as flag (for now)",
			in:   []string{"-x=1", "a"},
			want: []string{"-x=1", "a"},
		},
		{
			name: "lone dash is not a flag",
			in:   []string{"-", "a"},
			want: []string{"-", "a"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reorderFlags(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("reorderFlags(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
