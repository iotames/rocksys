// execlogstore_test.go：SQL 执行审计存储单测（sqlite 内存库，真实脚本建表）。
package adminapi

import (
	"testing"
	"time"

	"rocksys/internal/db"
)

// TestExecLogStoreInsertQuery 建表 + 批量落库 + 分页查询/计数闭环。
func TestExecLogStoreInsertQuery(t *testing.T) {
	d, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	defer func() { _ = d.Close() }()
	s := newExecLogStore(d.EasyDB(), d)
	if s == nil {
		t.Fatal("存储构造不应返回 nil（连接与脚本源均已就绪）")
	}

	batch := "0123456789abcdef0123456789abcdef"
	entries := []*ExecLogEntry{
		{Time: time.Now().UTC(), BatchID: batch, Seq: 1, SQLText: "CREATE TABLE t1 (id INTEGER)",
			OK: true, RowsAffected: 0, DurationMS: 3, ClientIP: "127.0.0.1", Source: "webui"},
		{Time: time.Now().UTC(), BatchID: batch, Seq: 2, SQLText: "CREATE TABLE t1 (id INTEGER)",
			OK: false, Error: "table t1 already exists", DurationMS: 1, ClientIP: "127.0.0.1", Source: "webui"},
	}
	if err := s.Insert(entries); err != nil {
		t.Fatalf("落审计批次: %v", err)
	}
	// 幂等性：再次落库（触发 ensure 重复路径）不应报错
	if err := s.Insert(entries[:1]); err != nil {
		t.Fatalf("重复落库（ensure 幂等）: %v", err)
	}

	total, err := s.Count()
	if err != nil {
		t.Fatalf("计数: %v", err)
	}
	if total != 3 {
		t.Fatalf("总数应为 3，got %d", total)
	}

	items, err := s.Query(2, 0)
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("第一页应为 2 条，got %d", len(items))
	}
	// id 倒序：最新插入的（entries[:1] 重复落库那条，seq=1 成功）在最前
	first := items[0]
	if !first.OK || first.Seq != 1 || first.BatchID != batch || first.ClientIP != "127.0.0.1" || first.Source != "webui" {
		t.Fatalf("首条字段不符: %+v", first)
	}
	// 第二条为失败记录：错误原因与结果位正确
	second := items[1]
	if second.OK || second.Error != "table t1 already exists" || second.DurationMS != 1 {
		t.Fatalf("失败记录字段不符: %+v", second)
	}
	if second.Time == "" {
		t.Fatal("time 应归一为非空字符串（RFC3339）")
	}

	// 分页 offset：跳过 2 条（重复落库的 seq=1 与失败 seq=2），只剩最早那条成功记录
	rest, err := s.Query(2, 2)
	if err != nil {
		t.Fatalf("翻页查询: %v", err)
	}
	if len(rest) != 1 || !rest[0].OK || rest[0].Seq != 1 {
		t.Fatalf("翻页应剩 1 条成功记录(seq=1)，got: %+v", rest)
	}
}

// TestNewExecLogStoreNilDeps 连接或脚本源缺失时降级为 nil（审计不可用、不阻断执行）。
func TestNewExecLogStoreNilDeps(t *testing.T) {
	if newExecLogStore(nil, nil) != nil {
		t.Fatal("连接为空时应返回 nil")
	}
	d, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	defer func() { _ = d.Close() }()
	if newExecLogStore(d.EasyDB(), nil) != nil {
		t.Fatal("脚本源为空时应返回 nil")
	}
}
