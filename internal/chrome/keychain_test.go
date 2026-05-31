package chrome

import (
	"slices"
	"testing"
)

func TestBuildPartitionListArgv_DefaultsToDefaultPartitionList(t *testing.T) {
	got := buildPartitionListArgv("", "")
	want := []string{
		"set-generic-password-partition-list",
		"-S", DefaultPartitionList,
		"-s", "Chrome Safe Storage",
		"-a", "Chrome",
	}
	if !slices.Equal(got, want) {
		t.Errorf("default argv: got %v, want %v", got, want)
	}
}

func TestBuildPartitionListArgv_CustomPartitionsPassthrough(t *testing.T) {
	custom := "apple-tool:,apple:,teamid:ABC1234"
	got := buildPartitionListArgv(custom, "")
	if got[2] != custom {
		t.Errorf("partition list arg: got %q, want %q", got[2], custom)
	}
	// Service and account stay pinned regardless of partition input.
	if got[4] != "Chrome Safe Storage" {
		t.Errorf("service arg: got %q", got[4])
	}
	if got[6] != "Chrome" {
		t.Errorf("account arg: got %q", got[6])
	}
}

func TestBuildPartitionListArgv_SubcommandIsFirst(t *testing.T) {
	// The `security` CLI dispatches on argv[0]. Mis-ordering here would
	// silently hit the wrong subcommand.
	got := buildPartitionListArgv("", "")
	if got[0] != "set-generic-password-partition-list" {
		t.Errorf("subcommand arg[0]: got %q, want set-generic-password-partition-list", got[0])
	}
}
