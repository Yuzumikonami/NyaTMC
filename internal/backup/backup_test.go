package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuzumikonami/nyatmc/internal/config"
	"github.com/yuzumikonami/nyatmc/internal/instance"
)

// newTestInstance 在临时数据目录里造一个带世界文件的实例。
func newTestInstance(t *testing.T) *instance.Instance {
	t.Helper()
	home := t.TempDir()
	config.SetHome(home)
	t.Cleanup(func() { config.SetHome("") })

	cfg := config.Default()
	cfg.Path = filepath.Join(home, "config.toml")
	if err := cfg.AddInstance("test", config.InstanceConfig{}); err != nil {
		t.Fatalf("登记实例失败: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	inst, err := instance.New(cfg, "test")
	if err != nil {
		t.Fatalf("构造实例失败: %v", err)
	}
	if err := inst.EnsureDirs(); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	// 世界文件 + 被排除的日志
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "level.dat"), "world-data")
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "region", "r.0.0.mca"), "region-data")
	writeTestFile(t, filepath.Join(inst.Res.Path, "server.properties"), "server-port=25565\n")
	writeTestFile(t, filepath.Join(inst.Res.Path, "logs", "latest.log"), "should-be-excluded")
	return inst
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateListFindRestore(t *testing.T) {
	inst := newTestInstance(t)

	ar, err := CreateDefault(inst, "unit", nil)
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if ar.Size <= 0 {
		t.Fatalf("备份文件大小为 %d", ar.Size)
	}
	if ar.Format != "tar.gz" {
		t.Errorf("格式 = %q，期望 tar.gz", ar.Format)
	}

	list, err := List(inst)
	if err != nil {
		t.Fatalf("列备份失败: %v", err)
	}
	if len(list) != 1 || list[0].Name != ar.Name {
		t.Fatalf("备份列表 = %+v", list)
	}

	found, err := Find(inst, "latest")
	if err != nil {
		t.Fatalf("Find(latest) 失败: %v", err)
	}
	if found.Name != ar.Name {
		t.Errorf("Find(latest) = %q，期望 %q", found.Name, ar.Name)
	}

	// 改动世界文件后回滚，内容必须恢复
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "level.dat"), "corrupted")
	res, err := Restore(inst, ar.Path, RestoreOptions{})
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if res.Files == 0 {
		t.Error("回滚文件数不应为 0")
	}
	got, err := os.ReadFile(filepath.Join(inst.Res.Path, "world", "level.dat"))
	if err != nil {
		t.Fatalf("读取恢复后的文件失败: %v", err)
	}
	if string(got) != "world-data" {
		t.Errorf("恢复后内容 = %q，期望 world-data", string(got))
	}
}

func TestExcludeIsHonored(t *testing.T) {
	inst := newTestInstance(t)
	ar, err := CreateDefault(inst, "exclude", nil)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(ar.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
	}
	for _, n := range names {
		if n == "logs/latest.log" || n == "logs" {
			t.Errorf("logs 目录应被排除，但备份里出现了 %q", n)
		}
	}
	want := map[string]bool{"world/level.dat": false, "world/region/r.0.0.mca": false, "server.properties": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("备份里缺少 %q（实际内容：%v）", name, names)
		}
	}
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	inst := newTestInstance(t)
	evil := filepath.Join(t.TempDir(), "evil.tar.gz")

	// 手工造一个含 ../ 的恶意备份
	f, err := os.Create(evil)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "../outside.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	if _, err := Restore(inst, evil, RestoreOptions{}); err == nil {
		t.Fatal("含路径穿越的备份必须被拒绝")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(inst.Res.Path), "outside.txt")); err == nil {
		t.Fatal("恶意文件被写到了服务器目录之外")
	}
}

func TestRestoreTargetsOnlySelectedPaths(t *testing.T) {
	inst := newTestInstance(t)
	ar, err := CreateDefault(inst, "partial", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "level.dat"), "changed")
	writeTestFile(t, filepath.Join(inst.Res.Path, "server.properties"), "changed=yes\n")

	if _, err := Restore(inst, ar.Path, RestoreOptions{Targets: []string{"world"}}); err != nil {
		t.Fatalf("部分回滚失败: %v", err)
	}
	level, _ := os.ReadFile(filepath.Join(inst.Res.Path, "world", "level.dat"))
	if string(level) != "world-data" {
		t.Errorf("world 应被恢复，实际 %q", string(level))
	}
	props, _ := os.ReadFile(filepath.Join(inst.Res.Path, "server.properties"))
	if string(props) != "changed=yes\n" {
		t.Errorf("未指定 world 之外的文件不应被覆盖，实际 %q", string(props))
	}
}

func TestRestoreBeforeBackup(t *testing.T) {
	inst := newTestInstance(t)
	ar, err := CreateDefault(inst, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "level.dat"), "newer")

	res, err := Restore(inst, ar.Path, RestoreOptions{BeforeBackup: true})
	if err != nil {
		t.Fatalf("带预备份的回滚失败: %v", err)
	}
	if res.Backup == nil {
		t.Fatal("应生成回滚前的自动备份")
	}
	if res.Backup.Label != "before-restore" {
		t.Errorf("自动备份标签 = %q，期望 before-restore", res.Backup.Label)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	inst := newTestInstance(t)
	base := time.Now()
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if _, err := Create(inst, Options{Label: "prune", Now: func() time.Time { return at }}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := List(inst)
	if len(list) != 4 {
		t.Fatalf("应有 4 份备份，实际 %d", len(list))
	}
	removed, err := Prune(inst, 2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("应删除 2 份，实际 %d", len(removed))
	}
	left, _ := List(inst)
	if len(left) != 2 {
		t.Fatalf("应保留 2 份，实际 %d", len(left))
	}
	// 留下的是最新的两份
	if !left[0].CreatedAt.After(left[1].CreatedAt) {
		t.Error("列表应按时间从新到旧排序")
	}
}

func TestZipFormat(t *testing.T) {
	inst := newTestInstance(t)
	ar, err := Create(inst, Options{Format: "zip", Label: "zip"})
	if err != nil {
		t.Fatalf("zip 备份失败: %v", err)
	}
	if ar.Format != "zip" {
		t.Errorf("格式 = %q，期望 zip", ar.Format)
	}
	writeTestFile(t, filepath.Join(inst.Res.Path, "world", "level.dat"), "broken")
	if _, err := Restore(inst, ar.Path, RestoreOptions{}); err != nil {
		t.Fatalf("zip 回滚失败: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(inst.Res.Path, "world", "level.dat"))
	if string(got) != "world-data" {
		t.Errorf("zip 回滚后内容 = %q", string(got))
	}
}

func TestCreateFailsWithoutContent(t *testing.T) {
	home := t.TempDir()
	config.SetHome(home)
	defer config.SetHome("")
	cfg := config.Default()
	cfg.Path = filepath.Join(home, "config.toml")
	_ = cfg.AddInstance("empty", config.InstanceConfig{})
	inst, _ := instance.New(cfg, "empty")
	_ = inst.EnsureDirs()

	if _, err := Create(inst, Options{Include: []string{"nonexistent"}}); err == nil {
		t.Fatal("没有任何可备份内容时应报错")
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		512:                "512B",
		1024:               "1KB",
		1024 * 1024:        "1MB",
		3 * 1024 * 1024:    "3MB",
		1024 * 1024 * 1024: "1GB",
		1536 * 1024:        "1.5MB",
	}
	for in, want := range cases {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q，期望 %q", in, got, want)
		}
	}
}
