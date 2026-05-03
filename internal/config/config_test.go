package config

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempConfig points the config dir at a fresh temp dir for the test.
func useTempConfig(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	return d
}

func TestPathUsesXDGConfigHome(t *testing.T) {
	d := useTempConfig(t)
	want := filepath.Join(d, "remolo", "config.json")
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	useTempConfig(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if c == nil {
		t.Fatal("Load returned nil config")
	}
	if len(c.Aliases) != 0 {
		t.Fatalf("expected empty aliases, got %d", len(c.Aliases))
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Fatalf("Load must not create the file, stat err = %v", err)
	}
}

func TestSetGetRemoveAlias(t *testing.T) {
	c := &Config{}

	if _, ok := c.GetAlias("workstation"); ok {
		t.Fatal("unexpected alias on empty config")
	}

	c.SetAlias("workstation", Alias{Token: "tok-123", Note: "laptop"})

	a, ok := c.GetAlias("workstation")
	if !ok {
		t.Fatal("alias not found after SetAlias")
	}
	if a.Token != "tok-123" || a.Note != "laptop" {
		t.Fatalf("alias mismatch: %+v", a)
	}

	if removed := c.RemoveAlias("missing"); removed {
		t.Fatal("RemoveAlias reported removal of missing alias")
	}
	if removed := c.RemoveAlias("workstation"); !removed {
		t.Fatal("RemoveAlias did not report removal of existing alias")
	}
	if _, ok := c.GetAlias("workstation"); ok {
		t.Fatal("alias still present after removal")
	}
}

func TestAliasNamesSorted(t *testing.T) {
	c := &Config{}
	c.SetAlias("zeta", Alias{})
	c.SetAlias("alpha", Alias{})
	c.SetAlias("mike", Alias{})

	got := c.AliasNames()
	want := []string{"alpha", "mike", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("AliasNames len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AliasNames = %v, want %v", got, want)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	useTempConfig(t)

	c := &Config{}
	c.SetAlias("workstation", Alias{
		Token:      "tok-abc",
		Rendezvous: "rv.example:443",
		Relay:      "relay.example:443",
		Note:       "home box",
	})
	c.Defaults = Defaults{Rendezvous: "default-rv:443"}
	c.Groups = map[string][]string{"work": {"workstation"}}

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("stat after Save: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perms = %o, want 600", perm)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a, ok := got.GetAlias("workstation")
	if !ok {
		t.Fatal("alias missing after round trip")
	}
	if a.Token != "tok-abc" || a.Rendezvous != "rv.example:443" || a.Relay != "relay.example:443" || a.Note != "home box" {
		t.Fatalf("alias round-trip mismatch: %+v", a)
	}
	if got.Defaults.Rendezvous != "default-rv:443" {
		t.Fatalf("defaults round-trip mismatch: %+v", got.Defaults)
	}
	members, ok := got.Group("work")
	if !ok || len(members) != 1 || members[0] != "workstation" {
		t.Fatalf("group round-trip mismatch: %v ok=%v", members, ok)
	}
	if _, ok := got.Group("missing"); ok {
		t.Fatal("Group reported a missing group as present")
	}
}

func TestSaveOverwriteDoesNotCorrupt(t *testing.T) {
	useTempConfig(t)

	first := &Config{}
	first.SetAlias("a", Alias{Token: "one"})
	if err := first.Save(); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	second := &Config{}
	second.SetAlias("b", Alias{Token: "two"})
	if err := second.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
	if _, ok := got.GetAlias("a"); ok {
		t.Fatal("old alias survived overwrite")
	}
	b, ok := got.GetAlias("b")
	if !ok || b.Token != "two" {
		t.Fatalf("overwritten config corrupt: %+v ok=%v", b, ok)
	}

	// No temp files should be left behind in the config dir.
	entries, err := os.ReadDir(filepath.Dir(Path()))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Fatalf("stray file left after Save: %q", e.Name())
		}
	}
}

func TestResolveKnownAlias(t *testing.T) {
	useTempConfig(t)

	c := &Config{}
	c.SetAlias("workstation", Alias{Token: "secret-token", Note: "n"})
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	token, alias, err := Resolve("workstation")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if token != "secret-token" {
		t.Fatalf("Resolve token = %q, want secret-token", token)
	}
	if alias == nil {
		t.Fatal("Resolve returned nil alias for known name")
	}
	if alias.Note != "n" {
		t.Fatalf("Resolve alias mismatch: %+v", alias)
	}
}

func TestResolveUnknownPassThrough(t *testing.T) {
	useTempConfig(t)

	raw := "this-looks-like-a-raw-token"
	token, alias, err := Resolve(raw)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if token != raw {
		t.Fatalf("Resolve token = %q, want %q", token, raw)
	}
	if alias != nil {
		t.Fatalf("Resolve returned non-nil alias for raw token: %+v", alias)
	}
}

func TestResolveWithCopyIsIndependent(t *testing.T) {
	c := &Config{}
	c.SetAlias("workstation", Alias{Token: "tok", Note: "orig"})

	_, alias := c.ResolveWith("workstation")
	if alias == nil {
		t.Fatal("ResolveWith returned nil for known alias")
	}
	alias.Note = "mutated"

	stored, _ := c.GetAlias("workstation")
	if stored.Note != "orig" {
		t.Fatalf("mutating returned alias affected stored config: %+v", stored)
	}
}
