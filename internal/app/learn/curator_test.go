package learn_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCurateRebuildsIndex(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, v, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	factDoc := "---\nslug: unindexed-fact\ntype: company\nconfidence: 5\nevidence_msg_id: m1\n---\n\nCông ty làm việc từ thứ Hai đến thứ Sáu.\n"
	if err := v.Write(ctx, "memory/facts/unindexed-fact.md", []byte(factDoc)); err != nil {
		t.Fatalf("write fact: %v", err)
	}
	_, _ = v.CommitPaths(ctx, []string{"memory/facts/unindexed-fact.md"}, "vault: seed unindexed fact")

	rep, err := svc.Curate(ctx, now)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if rep.IndexedFacts != 1 {
		t.Fatalf("IndexedFacts = %d, want 1", rep.IndexedFacts)
	}
	memRaw, _ := v.Read(ctx, "memory/MEMORY.md")
	if !strings.Contains(string(memRaw), "unindexed-fact") {
		t.Fatalf("MEMORY.md missing rebuilt fact line:\n%s", string(memRaw))
	}
}

func TestCurateRetiresSuperseded(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, v, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	oldDoc := "---\nslug: hq-old\ntype: company\nconfidence: 5\nevidence_msg_id: m1\nsuperseded_by: hq-new\n---\n\nVăn phòng cũ tại Quận 3.\n"
	newDoc := "---\nslug: hq-new\ntype: company\nconfidence: 5\nevidence_msg_id: m2\nsupersedes: hq-old\n---\n\nVăn phòng mới tại Quận 1.\n"
	_ = v.Write(ctx, "memory/facts/hq-old.md", []byte(oldDoc))
	_ = v.Write(ctx, "memory/facts/hq-new.md", []byte(newDoc))
	memInit := "# Memory Index\n\n- [hq-old](facts/hq-old.md): Văn phòng cũ tại Quận 3.\n- [hq-new](facts/hq-new.md): Văn phòng mới tại Quận 1.\n"
	_ = v.Write(ctx, "memory/MEMORY.md", []byte(memInit))
	_, _ = v.CommitPaths(ctx, []string{"memory/facts/hq-old.md", "memory/facts/hq-new.md", "memory/MEMORY.md"}, "vault: seed superseded")

	rep, err := svc.Curate(ctx, now)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if rep.RetiredFromIndex != 1 {
		t.Fatalf("RetiredFromIndex = %d, want 1", rep.RetiredFromIndex)
	}
	memAfter, _ := v.Read(ctx, "memory/MEMORY.md")
	if strings.Contains(string(memAfter), "hq-old") || !strings.Contains(string(memAfter), "hq-new") {
		t.Fatalf("unexpected MEMORY.md after retiring superseded:\n%s", string(memAfter))
	}
	if _, err := v.Read(ctx, "memory/facts/hq-old.md"); err != nil {
		t.Fatalf("superseded fact file must remain on disk: %v", err)
	}
}

func TestCurateNeverDeletesFacts(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, v, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		rel := fmt.Sprintf("memory/facts/fact-%d.md", i)
		sup := ""
		if i == 1 {
			sup = "superseded_by: fact-2\n"
		}
		doc := fmt.Sprintf("---\nslug: fact-%d\ntype: company\nconfidence: 4\nevidence_msg_id: m%d\n%s---\n\nNội dung %d.\n", i, i, sup, i)
		_ = v.Write(ctx, rel, []byte(doc))
	}
	_, _ = v.CommitPaths(ctx, []string{"memory/facts/fact-1.md", "memory/facts/fact-2.md", "memory/facts/fact-3.md"}, "vault: seed facts")

	if _, err := svc.Curate(ctx, now); err != nil {
		t.Fatalf("Curate: %v", err)
	}
	for i := 1; i <= 3; i++ {
		rel := fmt.Sprintf("memory/facts/fact-%d.md", i)
		if _, err := v.Read(ctx, rel); err != nil {
			t.Fatalf("Curate deleted %s: %v", rel, err)
		}
	}
}

func TestCurateWarnsOver150Lines(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, v, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	var lines []string
	lines = append(lines, "# Company")
	for i := 1; i <= 155; i++ {
		lines = append(lines, fmt.Sprintf("Dòng chính sách thứ %d", i))
	}
	bigCompany := strings.Join(lines, "\n") + "\n"
	_ = v.Write(ctx, "company/COMPANY.md", []byte(bigCompany))
	_, _ = v.CommitPaths(ctx, []string{"company/COMPANY.md"}, "vault: long company")

	rep, err := svc.Curate(ctx, now)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("expected warning when COMPANY.md exceeds 150 lines")
	}
	afterRaw, _ := v.Read(ctx, "company/COMPANY.md")
	if string(afterRaw) != bigCompany {
		t.Fatal("Curate must warn without auto-compressing or truncating COMPANY.md")
	}
}

func TestCurateLint(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, v, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	indexNote := "# Index\nTham khảo [[wiki/pricing]] và [[wiki/missing-page]].\n"
	pricingNote := "# Pricing\nChi tiết giá.\n"
	orphanNote := "# Orphan\nKhông có trang nào trỏ tới trang này.\n"
	_ = v.Write(ctx, "wiki/index.md", []byte(indexNote))
	_ = v.Write(ctx, "wiki/pricing.md", []byte(pricingNote))
	_ = v.Write(ctx, "wiki/orphan.md", []byte(orphanNote))
	_, _ = v.CommitPaths(ctx, []string{"wiki/index.md", "wiki/pricing.md", "wiki/orphan.md"}, "vault: wiki lint sample")

	rep, err := svc.Curate(ctx, now)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(rep.BrokenLinks) == 0 {
		t.Fatalf("expected BrokenLinks to report wiki/missing-page, got %+v", rep)
	}
	if len(rep.OrphanNotes) == 0 {
		t.Fatalf("expected OrphanNotes to report wiki/orphan.md, got %+v", rep)
	}
	afterIndex, _ := v.Read(ctx, "wiki/index.md")
	if string(afterIndex) != indexNote {
		t.Fatal("Curate lint must not modify wiki files")
	}
}

func TestCurateSkipsUnchangedVault(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	_, _, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return now })
	ctx := context.Background()

	rep1, err := svc.Curate(ctx, now)
	if err != nil || rep1.Skipped {
		t.Fatalf("first Curate = (%+v, %v), want Skipped=false", rep1, err)
	}
	rep2, err := svc.Curate(ctx, now.Add(time.Minute))
	if err != nil || !rep2.Skipped {
		t.Fatalf("second Curate on unchanged vault = (%+v, %v), want Skipped=true", rep2, err)
	}
}
