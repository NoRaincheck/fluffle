package agentcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	entries, err := Parse([]byte("[[agents]]\nname = \"probe\"\ncommand = \"/bin/probe\"\n"), "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	got := entries[0]
	if got.Reply != "auto" {
		t.Fatalf("Reply = %q, want auto", got.Reply)
	}
	if got.TimeoutSecs != 300 {
		t.Fatalf("TimeoutSecs = %d, want 300", got.TimeoutSecs)
	}
	if got.Source != "/tmp/x.toml" {
		t.Fatalf("Source = %q", got.Source)
	}
	if got.Args != nil {
		t.Fatalf("Args = %#v, want nil", got.Args)
	}
}

func TestParseFullEntry(t *testing.T) {
	src := `
[[agents]]
name = "reviewer"
description = "reviews the diff"
command = "claude"
args = ["-p", "{prompt}"]
reply = "cli"
timeout_secs = 42
env = { FOO = "bar" }
system_prompt = "be concrete"
`
	entries, err := Parse([]byte(src), "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{Agent: Agent{
		Name:         "reviewer",
		Description:  "reviews the diff",
		Command:      "claude",
		Args:         []string{"-p", "{prompt}"},
		Reply:        "cli",
		TimeoutSecs:  42,
		Env:          map[string]string{"FOO": "bar"},
		SystemPrompt: "be concrete",
	}, Source: "/tmp/x.toml"}
	if !reflect.DeepEqual(entries[0], want) {
		t.Fatalf("got  %+v\nwant %+v", entries[0], want)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, tc := range []struct{ name, src, wantErr string }{
		{"missing name", "[[agents]]\ncommand = \"x\"\n", "name"},
		{"blank name", "[[agents]]\nname = \"   \"\ncommand = \"x\"\n", "name"},
		{"missing command", "[[agents]]\nname = \"a\"\n", "command"},
		{"uppercase name", "[[agents]]\nname = \"Reviewer\"\ncommand = \"x\"\n", "name"},
		{"name starting with dash", "[[agents]]\nname = \"-a\"\ncommand = \"x\"\n", "name"},
		{"name starting with underscore", "[[agents]]\nname = \"_a\"\ncommand = \"x\"\n", "name"},
		{"bad reply", "[[agents]]\nname=\"a\"\ncommand=\"x\"\nreply=\"nope\"\n", "reply"},
		{"zero timeout", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=0\n", "timeout_secs"},
		{"negative timeout", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=-1\n", "timeout_secs"},
		{"timeout overflows a duration", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=9223372037\n", "timeout_secs"},
		{"unknown key", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_second=5\n", "unknown"},
		{"agents not a table", "agents = 1\n", "agents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "/tmp/x.toml")
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseRejectsTimeoutThatOverflowsADuration(t *testing.T) {
	overflow := 9223372037
	overflowed := time.Duration(overflow) * time.Second
	if overflowed > 0 {
		t.Fatalf("premise broken: %d seconds must overflow a duration", overflow)
	}
	_, err := Parse([]byte("[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=9223372037\n"), "/tmp/x.toml")
	if err == nil {
		t.Fatalf("timeout_secs=%d must be rejected: it wraps negative, so the run would time out before it starts", overflow)
	}
	if !strings.Contains(err.Error(), "timeout_secs") {
		t.Fatalf("error = %q, want it to name timeout_secs", err.Error())
	}
}

func TestParseAcceptsLargestTimeoutThatFitsADuration(t *testing.T) {
	entries, err := Parse([]byte("[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=9223372036\n"), "/tmp/x.toml")
	if err != nil {
		t.Fatalf("the largest representable timeout must be accepted: %v", err)
	}
	if d := time.Duration(entries[0].TimeoutSecs) * time.Second; d <= 0 {
		t.Fatalf("Duration = %d ns, want a positive deadline", int64(d))
	}
}

func TestParseTimeoutPresenceIsPerEntry(t *testing.T) {
	src := "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=7\n\n[[agents]]\nname=\"b\"\ncommand=\"x\"\n"
	entries, err := Parse([]byte(src), "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].TimeoutSecs != 7 {
		t.Fatalf("a.TimeoutSecs = %d, want 7", entries[0].TimeoutSecs)
	}
	if entries[1].TimeoutSecs != 300 {
		t.Fatalf("b.TimeoutSecs = %d, want 300", entries[1].TimeoutSecs)
	}
	_, err = Parse([]byte("[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=0\n"), "/tmp/x.toml")
	if err == nil {
		t.Fatal("explicit timeout_secs=0 must not be silently defaulted")
	}
}

func TestParseEmptyIsNotAnError(t *testing.T) {
	entries, err := Parse(nil, "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestResolveRepoOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	repoDir := filepath.Join(dir, "repo")
	repo := filepath.Join(repoDir, ".flf.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"global-a\"\n\n[[agents]]\nname=\"shared\"\ncommand=\"global-shared\"\n")
	writeFile(t, repo, "[[agents]]\nname=\"shared\"\ncommand=\"repo-shared\"\nreply=\"cli\"\n\n[[agents]]\nname=\"b\"\ncommand=\"repo-b\"\n")

	set, err := NewLoader(global).Resolve(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	shared, ok := set.Lookup("shared")
	if !ok {
		t.Fatal("shared missing")
	}
	if shared.Command != "repo-shared" || shared.Reply != "cli" {
		t.Fatalf("repo did not win: %+v", shared)
	}
	a, _ := set.Lookup("a")
	if a.Command != "global-a" || a.Source != global {
		t.Fatalf("global-only entry lost: %+v", a)
	}
	b, _ := set.Lookup("b")
	if b.Command != "repo-b" {
		t.Fatalf("repo-only entry lost: %+v", b)
	}
	if len(set.Entries()) != 3 {
		t.Fatalf("entries = %d, want 3", len(set.Entries()))
	}
	names := []string{}
	for _, e := range set.Entries() {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b", "shared"}) {
		t.Fatalf("Entries not sorted by name: %v", names)
	}
}

func TestResolveMissingRepoFileFallsBackToGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"g\"\n")
	set, err := NewLoader(global).Resolve(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Lookup("a"); !ok {
		t.Fatal("global entry missing")
	}
}

func TestResolveOrphanUsesGlobalOnly(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"g\"\n")
	set, err := NewLoader(global).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Entries()) != 1 {
		t.Fatalf("entries = %+v", set.Entries())
	}
}

func TestResolveMissingGlobalIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	set, err := NewLoader(filepath.Join(dir, "absent.toml")).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Entries()) != 0 {
		t.Fatalf("entries = %+v", set.Entries())
	}
}

func TestResolvePropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"BAD\"\ncommand=\"x\"\n")
	if _, err := NewLoader(global).Resolve(""); err == nil {
		t.Fatal("expected a validation error from Resolve")
	}
}

func TestResolvePicksUpRewrite(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"first\"\n")
	l := NewLoader(global)
	if _, err := l.Resolve(""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"second\"\n")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(global, later, later); err != nil {
		t.Fatal(err)
	}
	set, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := set.Lookup("a")
	if a.Command != "second" {
		t.Fatalf("stale cache: %+v", a)
	}
}

func TestResolveDropsCacheWhenFileRemoved(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"g\"\n")
	stamp, err := os.Stat(global)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLoader(global)
	if set, err := l.Resolve(""); err != nil {
		t.Fatal(err)
	} else if len(set.Entries()) != 1 {
		t.Fatalf("entries = %+v", set.Entries())
	}
	if err := os.Remove(global); err != nil {
		t.Fatal(err)
	}
	set, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Entries()) != 0 {
		t.Fatalf("stale cache after removal: %+v", set.Entries())
	}
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"recreated\"\n")
	if err := os.Chtimes(global, stamp.ModTime(), stamp.ModTime()); err != nil {
		t.Fatal(err)
	}
	set, err = l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := set.Lookup("a")
	if a.Command != "recreated" {
		t.Fatalf("stale cache after recreation: %+v", a)
	}
}

func TestResolveServesCachedParseWhenModTimeUnchanged(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"first\"\n")
	stamp, err := os.Stat(global)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLoader(global)
	if _, err := l.Resolve(""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"second\"\n")
	if err := os.Chtimes(global, stamp.ModTime(), stamp.ModTime()); err != nil {
		t.Fatal(err)
	}
	set, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := set.Lookup("a")
	if a.Command != "first" {
		t.Fatalf("cache did not serve: %+v", a)
	}
}

func TestResolveClonesMutableFieldsPerSet(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"c\"\nargs=[\"one\"]\nenv={ K = \"v\" }\n")
	l := NewLoader(global)
	first, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := first.Lookup("a")
	if !ok {
		t.Fatal("a missing")
	}
	e.Args[0] = "mutated"
	e.Env["K"] = "mutated"

	second, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := second.Lookup("a")
	if got.Args[0] != "one" {
		t.Fatalf("Args aliased across Resolve calls: %#v", got.Args)
	}
	if got.Env["K"] != "v" {
		t.Fatalf("Env aliased across Resolve calls: %#v", got.Env)
	}
}
