package versionutil

import "testing"

func TestVersionParsing(t *testing.T) {
	cases := []struct {
		Test     string
		Expected Version
	}{
		{
			Test: "0.8.1",
			Expected: Version{
				Name:          "0.8.1",
				VersionNumber: [3]int{0, 8, 1},
			},
		},
		{
			Test: "0.8.1-dev",
			Expected: Version{
				Name:          "0.8.1-dev",
				VersionNumber: [3]int{0, 8, 1},
				Tag:           "dev",
			},
		},
		{
			Test: "v0.8.1-dev",
			Expected: Version{
				Name:          "v0.8.1-dev",
				VersionNumber: [3]int{0, 8, 1},
				Tag:           "dev",
			},
		},
		{
			Test: "v0.8.1-rc1",
			Expected: Version{
				Name:          "v0.8.1-rc1",
				VersionNumber: [3]int{0, 8, 1},
				Tag:           "rc1",
			},
		},
		{
			Test: "v0.8.1-dev@aaffbb1234",
			Expected: Version{
				Name:          "v0.8.1-dev@aaffbb1234",
				VersionNumber: [3]int{0, 8, 1},
				Tag:           "dev",
				Commit:        "aaffbb1234",
			},
		},
	}
	for _, tc := range cases {
		v, err := ParseVersion(tc.Test)
		if err != nil {
			t.Fatal(err)
		}
		if v != tc.Expected {
			t.Errorf("Mismatched version value\n\tActual: %#v\n\tExpected: %#v", v, tc.Expected)
		}
	}
}

func TestOrdering(t *testing.T) {
	cases := []struct {
		Before string
		After  string
	}{
		{
			Before: "0.8.1",
			After:  "0.8.2",
		},
		{
			Before: "0.8.1-rc1",
			After:  "0.8.1",
		},
		{
			Before: "0.8.1-rc1",
			After:  "0.8.1-rc2",
		},
		{
			Before: "0.8.1-dev",
			After:  "0.8.1-rc1",
		},
		{
			Before: "0.8.1-dev",
			After:  "0.8.1",
		},
		{
			Before: "0.8.1",
			After:  "0.8.2-dev",
		},
		{
			Before: "0.8.1-other",
			After:  "0.8.1",
		},
		{
			Before: "0.8.1-dev",
			After:  "0.8.1-aaa",
		},
	}
	for _, tc := range cases {
		v1, err := ParseVersion(tc.Before)
		if err != nil {
			t.Fatal(err)
		}
		v2, err := ParseVersion(tc.After)
		if err != nil {
			t.Fatal(err)
		}
		if !v1.LessThan(v2) {
			t.Fatalf("Expected %v to be less than %v", tc.Before, tc.After)
		}
	}
}
