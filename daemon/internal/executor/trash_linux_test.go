//go:build linux

// Trash flow on a real filesystem: delete-with-trash → restore → purge.
package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
)

func TestTrashMoveRestorePurge(t *testing.T) {
	e, src, _ := testEnv(t)
	write(t, src, "a.txt", "hello trash")

	man := buildManifest(t, e, model.ActDelete, "src", "a.txt", "src", "", []planner.Item{
		statItem(t, e, "src", "a.txt", model.EntryFile),
	})
	man.Trash = true

	var moved []TrashRecord
	out, err := e.DeleteWithTrash(man, noHooks(), func(r []TrashRecord) { moved = r })
	if err != nil {
		t.Fatalf("trash delete: %v", err)
	}
	if out.Items != 1 || len(moved) != 1 {
		t.Fatalf("unexpected outcome: %+v moved=%+v", out, moved)
	}
	// 原位置已无文件
	if _, err := os.Stat(filepath.Join(src, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("source must be gone after trash move")
	}
	// 回收站里有 entry
	_, infos, err := e.TrashList(model.RootRef{Kind: model.KindShare, ID: "src"})
	if err != nil || len(infos) != 1 {
		t.Fatalf("trash list: %v %+v", err, infos)
	}
	if infos[0].OriginRel != "a.txt" {
		t.Fatalf("origin mismatch: %+v", infos[0])
	}
	// 恢复
	restored, err := e.TrashRestore(model.RootRef{Kind: model.KindShare, ID: "src"}, infos[0].Name)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != "a.txt" {
		t.Fatalf("restored name: %q", restored)
	}
	got, err := os.ReadFile(filepath.Join(src, "a.txt"))
	if err != nil || string(got) != "hello trash" {
		t.Fatalf("restored content: %q err=%v", got, err)
	}
	// 彻底删除（purge = Trash=false 的普通删除）
	man2 := buildManifest(t, e, model.ActDelete, "src", "a.txt", "src", "", []planner.Item{
		statItem(t, e, "src", "a.txt", model.EntryFile),
	})
	man2.Trash = false
	if _, err := e.Delete(man2, noHooks()); err != nil {
		t.Fatalf("purge delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("purge must remove the file")
	}
}

func TestTrashNameConflictAutoRename(t *testing.T) {
	e, src, _ := testEnv(t)
	write(t, src, "a.txt", "first")
	write(t, src, "b.txt", "second")

	del := func(rel string) {
		t.Helper()
		man := buildManifest(t, e, model.ActDelete, "src", rel, "src", "", []planner.Item{
			statItem(t, e, "src", rel, model.EntryFile),
		})
		man.Trash = true
		if _, err := e.DeleteWithTrash(man, noHooks(), nil); err != nil {
			t.Fatalf("trash delete %s: %v", rel, err)
		}
	}
	del("a.txt")

	_, infos1, err := e.TrashList(model.RootRef{Kind: model.KindShare, ID: "src"})
	if err != nil || len(infos1) != 1 {
		t.Fatalf("trash list: %v %+v", err, infos1)
	}
	// 恢复 a.txt 回原位（内容 "first"）
	if _, err := e.TrashRestore(model.RootRef{Kind: model.KindShare, ID: "src"}, infos1[0].Name); err != nil {
		t.Fatalf("restore 1: %v", err)
	}

	// 原位置再次被占用：写入不同内容，再删（进回收站）。
	// 恢复时 "a.txt" 已被占用 → 冲突自动改名 "a (1).txt"。
	write(t, src, "a.txt", "second version")
	del("a.txt")
	_, infos2, err := e.TrashList(model.RootRef{Kind: model.KindShare, ID: "src"})
	if err != nil || len(infos2) != 1 {
		t.Fatalf("trash list 2: %v %+v", err, infos2)
	}
	restored2, err := e.TrashRestore(model.RootRef{Kind: model.KindShare, ID: "src"}, infos2[0].Name)
	if err != nil {
		t.Fatalf("restore 2: %v", err)
	}
	if restored2 != "a (1).txt" {
		t.Fatalf("conflict restore should auto-rename to a (1).txt, got %q", restored2)
	}
	got, _ := os.ReadFile(filepath.Join(src, "a.txt"))
	got2, _ := os.ReadFile(filepath.Join(src, "a (1).txt"))
	if string(got) != "first" || string(got2) != "second version" {
		t.Fatalf("restore content mixup: a.txt=%q a(1)=%q", got, got2)
	}
}

func TestTrashIdentityCheckKeepsChangedSource(t *testing.T) {
	e, src, _ := testEnv(t)
	write(t, src, "hot.txt", "v1")

	it := statItem(t, e, "src", "hot.txt", model.EntryFile)
	it.Ino += 12345 // 身份不再匹配
	man := buildManifest(t, e, model.ActDelete, "src", "hot.txt", "src", "", []planner.Item{it})
	man.Trash = true

	out, err := e.DeleteWithTrash(man, noHooks(), nil)
	if err != nil {
		t.Fatalf("trash delete: %v", err)
	}
	if out.Warnings == 0 {
		t.Fatal("expected a warning for the changed source")
	}
	if _, err := os.Stat(filepath.Join(src, "hot.txt")); err != nil {
		t.Fatal("changed source must remain")
	}
}
