package replication

import "testing"

func TestLastWriterWins(t *testing.T) {
	r := LastWriterWins{}

	local := ChangeEntry{SiteID: "site-1", Timestamp: 100}
	remote := ChangeEntry{SiteID: "site-2", Timestamp: 200}

	if got := r.Resolve(local, remote); got.SiteID != "site-2" {
		t.Fatalf("later remote should win, got %q", got.SiteID)
	}
	if got := r.Resolve(remote, local); got.SiteID != "site-2" {
		t.Fatalf("later local should win regardless of arg order, got %q", got.SiteID)
	}
}

func TestLastWriterWinsTieBreak(t *testing.T) {
	r := LastWriterWins{}
	local := ChangeEntry{SiteID: "site-1", Timestamp: 100}
	remote := ChangeEntry{SiteID: "site-2", Timestamp: 100}

	// Equal timestamps → deterministic higher-site-ID wins, independent of order.
	if got := r.Resolve(local, remote); got.SiteID != "site-2" {
		t.Fatalf("tie should go to higher site ID, got %q", got.SiteID)
	}
	if got := r.Resolve(remote, local); got.SiteID != "site-2" {
		t.Fatalf("tie-break must be order-independent, got %q", got.SiteID)
	}
}

func TestLargestObject(t *testing.T) {
	r := LargestObject{}

	put := ChangeEntry{EventType: "put", Size: 10, SiteID: "s1"}
	del := ChangeEntry{EventType: "delete", SiteID: "s2"}

	// Put always beats delete, whichever side it's on.
	if got := r.Resolve(del, put); got.EventType != "put" {
		t.Fatal("put should beat delete (remote put)")
	}
	if got := r.Resolve(put, del); got.EventType != "put" {
		t.Fatal("put should beat delete (local put)")
	}

	// Two puts: larger wins.
	small := ChangeEntry{EventType: "put", Size: 5, ETag: "a"}
	large := ChangeEntry{EventType: "put", Size: 50, ETag: "b"}
	if got := r.Resolve(small, large); got.Size != 50 {
		t.Fatalf("larger object should win, got size %d", got.Size)
	}

	// Equal size → higher ETag tie-break.
	e1 := ChangeEntry{EventType: "put", Size: 5, ETag: "aaa"}
	e2 := ChangeEntry{EventType: "put", Size: 5, ETag: "zzz"}
	if got := r.Resolve(e1, e2); got.ETag != "zzz" {
		t.Fatalf("ETag tie-break failed, got %q", got.ETag)
	}
}

func TestSitePreference(t *testing.T) {
	r := SitePreference{PreferredSiteID: "site-2"}

	local := ChangeEntry{SiteID: "site-1", Timestamp: 999} // newer but not preferred
	remote := ChangeEntry{SiteID: "site-2", Timestamp: 1}

	if got := r.Resolve(local, remote); got.SiteID != "site-2" {
		t.Fatalf("preferred site should win even when older, got %q", got.SiteID)
	}

	// When the remote is not the preferred site, local stays.
	other := ChangeEntry{SiteID: "site-3", Timestamp: 5}
	if got := r.Resolve(local, other); got.SiteID != "site-1" {
		t.Fatalf("non-preferred remote should lose, got %q", got.SiteID)
	}
}

func TestNewConflictResolver(t *testing.T) {
	cases := []struct {
		strategy ConflictStrategy
		pref     string
		wantType string
	}{
		{StrategyLastWriterWins, "", "replication.LastWriterWins"},
		{StrategyLargestObject, "", "replication.LargestObject"},
		{StrategySitePreference, "site-9", "replication.SitePreference"},
		{"bogus-unknown", "", "replication.LastWriterWins"}, // default
	}
	for _, c := range cases {
		r := NewConflictResolver(c.strategy, c.pref)
		if got := typeName(r); got != c.wantType {
			t.Fatalf("strategy %q → %s, want %s", c.strategy, got, c.wantType)
		}
	}

	// site-preference resolver must carry the configured site.
	sp, ok := NewConflictResolver(StrategySitePreference, "site-9").(SitePreference)
	if !ok || sp.PreferredSiteID != "site-9" {
		t.Fatal("site-preference resolver did not retain preferred site")
	}
}

func typeName(v interface{}) string {
	switch v.(type) {
	case LastWriterWins:
		return "replication.LastWriterWins"
	case LargestObject:
		return "replication.LargestObject"
	case SitePreference:
		return "replication.SitePreference"
	default:
		return "unknown"
	}
}
