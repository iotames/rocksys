// Package mq 见 doc.go。本文件实现 RockMQ：异步消息可靠投递（不依赖独立 MQ）。
// 依据 DEV_HANDBOOK.md 第 18 章：OutboxStore（业务库内 outbox 表）、
// PollingDeliverer（定时轮询 + HTTP POST 投递）、MQ（hotswap 独立组件，不挂 chain）。
//
// 表结构（业务库内，由 EnsureTable 幂等创建）：
//
//	id          INTEGER PRIMARY KEY AUTOINCREMENT,
//	topic       TEXT NOT NULL,                 -- 消费主题
//	payload     TEXT NOT NULL,                 -- 消息负载
//	status      TEXT NOT NULL DEFAULT 'pending', -- pending/failed/done/dead
//	retry_count INTEGER NOT NULL DEFAULT 0,    -- 手册最小集合之外，用于失败重试计数
//	last_error  TEXT NOT NULL DEFAULT '',      -- 最近一次失败原因（可观测性）
//	created_at  DATETIME NOT NULL
//
// 核心流程：stbiz 本地事务写业务表 + outbox 同事务 → 轮询器取
// status in (pending, failed) → HTTP POST {topic, payload} 投递 → 消费方幂等处理
// → 2xx 则 MarkDone；失败则 MarkFailed（重试计数+1），超最大次数（默认 3）转死信
// （MarkDead），期间按 2^retry * base 指数退避。
package mq

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iotames/easydb"
	"github.com/iotames/easyserver/log"

	"rocksys/internal/db"
	"rocksys/internal/hotswap"
)

// 默认参数（§18）。
const (
	defaultInterval    = time.Second
	defaultBaseBackoff = 100 * time.Millisecond
	defaultMaxRetries  = 3
	defaultFetchLimit  = 100
	defaultHTTPTimeout = 5 * time.Second
)

// 消息状态。
const (
	statusPending = "pending"
	statusFailed  = "failed"
	statusDone    = "done"
	statusDead    = "dead"
)

// Message 一条待/已投递消息（对应 outbox 一行）。
type Message struct {
	ID         int64     // 自增主键
	Topic      string    // 消费主题
	Payload    string    // 负载
	Status     string    // pending/failed/done/dead
	RetryCount int       // 已失败（重试）次数
	CreatedAt  time.Time // 入库时间
}

// OutboxStore 业务库内 outbox 表访问层（§18）。
// 数据操作经 easydb 执行；SQL 语句从 sql/<dbtype>/ 目录脚本读取（外置优先、嵌入兜底）。
type OutboxStore struct {
	edb        *easydb.EasyDb
	sqls       db.SQLSource // SQL 脚本源；nil 时无法执行任何 SQL 操作
	tableName  string
	maxRetries int
}

// NewOutboxStore 创建基于 db 的 outbox 存储；tableName 为 outbox 表名。
// 不自动建表（构造函数不返回 error），请调用 EnsureTable。
// 注意：使用前需调用 SetSQLSource 注入 SQL 脚本源（SQL 外置到 sql 目录）。
func NewOutboxStore(db *sql.DB, tableName string) *OutboxStore {
	return &OutboxStore{edb: easydb.NewEasyDbBySqlDB(db), tableName: tableName, maxRetries: defaultMaxRetries}
}

// SetSQLSource 注入 SQL 脚本源（通常为 internal/db 数据访问层）。
func (s *OutboxStore) SetSQLSource(src db.SQLSource) {
	s.sqls = src
}

// SetMaxRetries 设置失败判定死信的最大次数；n<=0 时忽略保持默认。
func (s *OutboxStore) SetMaxRetries(n int) {
	if n > 0 {
		s.maxRetries = n
	}
}

// sqlText 从脚本源读取 SQL 文本并替换 {table} 表名占位符。
func (s *OutboxStore) sqlText(name string) (string, error) {
	if s.sqls == nil {
		return "", fmt.Errorf("mq: 未设置 SQL 脚本源（请调用 SetSQLSource 注入 internal/db）")
	}
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("mq: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", s.tableName), nil
}

// EnsureTable 幂等建表（含 status 索引）。
func (s *OutboxStore) EnsureTable() error {
	ddl, err := s.sqlText("mq_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := s.edb.Exec(ddl); err != nil {
		return err
	}
	idx, err := s.sqlText("mq_create_index.sql")
	if err != nil {
		return err
	}
	// 多语句脚本逐条执行 + 幂等容错：MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，
	// 重复执行报 "Duplicate key name"——该错误忽略（与 obs 组件索引逻辑一致）。
	for _, stmt := range db.SplitSQLStatements(idx) {
		if _, err := s.edb.Exec(stmt); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
				continue
			}
			return err
		}
	}
	return nil
}

// Insert 写入一条 pending 消息；返回新行自增 id。
// PostgreSQL 驱动（lib/pq）不支持 Result.LastInsertId，故方言判断后走
// mq_insert_returning_id.sql（RETURNING id）取自增 id；其余方言用普通 INSERT + LastInsertId。
func (s *OutboxStore) Insert(topic, payload string) (int64, error) {
	created := time.Now().UTC().Format(time.RFC3339)
	if da, ok := s.sqls.(interface{ Driver() string }); ok && da.Driver() == "postgres" {
		ret, err := s.sqlText("mq_insert_returning_id.sql")
		if err != nil {
			return 0, err
		}
		var id int64
		if err := s.edb.QueryRow(ret, topic, payload, created).Scan(&id); err != nil {
			return 0, fmt.Errorf("mq: 插入消息失败（RETURNING id）: %w", err)
		}
		return id, nil
	}
	stmt, err := s.sqlText("mq_insert.sql")
	if err != nil {
		return 0, err
	}
	res, err := s.edb.Exec(stmt, topic, payload, created)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FetchPending 取出待投递/待重试消息（status in pending,failed），按 id 升序，最多 limit 条。
func (s *OutboxStore) FetchPending(limit int) ([]Message, error) {
	if limit <= 0 {
		limit = defaultFetchLimit
	}
	stmt, err := s.sqlText("mq_fetch_pending.sql")
	if err != nil {
		return nil, err
	}
	rows, err := s.edb.Query(stmt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]Message, 0, 8)
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.Topic, &m.Payload, &m.Status, &m.RetryCount, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, created); err == nil {
			m.CreatedAt = t
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// MarkDone 标记消息已成功投递。
func (s *OutboxStore) MarkDone(id int64) error {
	stmt, err := s.sqlText("mq_mark_done.sql")
	if err != nil {
		return err
	}
	_, err = s.edb.Exec(stmt, id)
	return err
}

// MarkFailed 记录一次失败：retry_count+1；若更新后达到最大重试次数则转死信（dead），
// 否则置为 failed（下次轮询重试）。返回更新后的重试次数。
func (s *OutboxStore) MarkFailed(id int64, errMsg string) (int, error) {
	stmt, err := s.sqlText("mq_mark_failed.sql")
	if err != nil {
		return 0, err
	}
	if _, err := s.edb.Exec(stmt, s.maxRetries, errMsg, id); err != nil {
		return 0, err
	}
	var c int
	q, err := s.sqlText("mq_get_retry_count.sql")
	if err != nil {
		return 0, err
	}
	err = s.edb.QueryRow(q, id).Scan(&c)
	return c, err
}

// MarkDead 强制标记为死信。
func (s *OutboxStore) MarkDead(id int64) error {
	stmt, err := s.sqlText("mq_mark_dead.sql")
	if err != nil {
		return err
	}
	_, err = s.edb.Exec(stmt, id)
	return err
}

// PollingDeliverer 定时轮询 outbox + HTTP POST 投递到消费方（§18）。
// 消费方地址：routes[topic] 优先，否则用 ConsumerBaseURL；两者皆无则报错。
type PollingDeliverer struct {
	store       *OutboxStore
	interval    time.Duration
	client      *http.Client
	maxRetries  int
	baseBackoff time.Duration

	mu      sync.RWMutex
	routes  map[string]string // topic -> 消费方 URL
	baseURL string            // 默认消费方 base URL

	running atomic.Bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

// NewPollingDeliverer 创建投递器。interval <= 0 时使用默认 1s。
func NewPollingDeliverer(store *OutboxStore, interval time.Duration) *PollingDeliverer {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &PollingDeliverer{
		store:       store,
		interval:    interval,
		client:      &http.Client{Timeout: defaultHTTPTimeout},
		maxRetries:  defaultMaxRetries,
		baseBackoff: defaultBaseBackoff,
		routes:      make(map[string]string),
	}
}

// SetMaxRetries 设置最大重试次数（用于死信判定与退避封顶）。
func (d *PollingDeliverer) SetMaxRetries(n int) {
	if n > 0 {
		d.maxRetries = n
	}
}

// SetBaseBackoff 设置指数退避基数（睡眠 = 2^(retry-1) × base）。
func (d *PollingDeliverer) SetBaseBackoff(b time.Duration) {
	if b > 0 {
		d.baseBackoff = b
	}
}

// SetConsumerBaseURL 设置默认消费方地址（未命中路由时使用）。
func (d *PollingDeliverer) SetConsumerBaseURL(url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.baseURL = url
}

// SetRoute 为指定 topic 设置独立消费方 URL。
func (d *PollingDeliverer) SetRoute(topic, url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.routes[topic] = url
}

// Start 启动轮询 goroutine；幂等。
func (d *PollingDeliverer) Start() error {
	if d.running.Swap(true) {
		return nil
	}
	d.stop = make(chan struct{})
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-ticker.C:
				d.pollOnce()
			}
		}
	}()
	return nil
}

// Stop 停止轮询并等待 goroutine 退出；幂等。
func (d *PollingDeliverer) Stop() error {
	if !d.running.Swap(false) {
		return nil
	}
	close(d.stop)
	d.wg.Wait()
	return nil
}

// pollOnce 单轮轮询：取待投递消息并逐条投递。
func (d *PollingDeliverer) pollOnce() {
	msgs, err := d.store.FetchPending(defaultFetchLimit)
	if err != nil {
		log.Error("mq: 轮询获取待投递消息失败", "error", err.Error())
		return
	}
	for _, m := range msgs {
		if err := d.deliver(m); err != nil {
			count, ferr := d.store.MarkFailed(m.ID, err.Error())
			if ferr != nil {
				log.Error("mq: 记录投递失败失败", "id", m.ID, "err", ferr.Error())
				continue
			}
			if count >= d.maxRetries {
				log.Warn("mq: 投递失败次数超限转死信", "id", m.ID, "topic", m.Topic, "retry", count)
				continue
			}
			if !d.sleep(d.baseBackoff * time.Duration(1<<(count-1))) {
				return // 被 Stop 打断
			}
			continue
		}
		if err := d.store.MarkDone(m.ID); err != nil {
			log.Error("mq: 标记投递成功失败", "id", m.ID, "err", err.Error())
		}
	}
}

// deliver 对单条消息 HTTP POST 到目标消费方；2xx 视为成功。
func (d *PollingDeliverer) deliver(msg Message) error {
	target, err := d.urlFor(msg.Topic)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"topic": msg.Topic, "payload": msg.Payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("消费方返回非 2xx 状态码: %d", resp.StatusCode)
	}
	return nil
}

// urlFor 解析消费方目标 URL：按 topic 路由优先，其次默认 baseURL。
func (d *PollingDeliverer) urlFor(topic string) (string, error) {
	d.mu.RLock()
	u, ok := d.routes[topic]
	base := d.baseURL
	d.mu.RUnlock()
	if ok && u != "" {
		return u, nil
	}
	if base != "" {
		return base, nil
	}
	return "", fmt.Errorf("未配置消费方地址（topic=%s）", topic)
}

// sleep 休眠 dur，被 Stop 打断时返回 false。
func (d *PollingDeliverer) sleep(dur time.Duration) bool {
	select {
	case <-time.After(dur):
		return true
	case <-d.stop:
		return false
	}
}

// Options MQ 组件启动配置（MQ.Start 的 cfg 参数，可空）。
type Options struct {
	Interval        time.Duration     // 轮询间隔；0 用默认 1s
	ConsumerBaseURL string            // 默认消费方地址
	Routes          map[string]string // topic -> 消费方 URL
	MaxRetries      int               // 最大重试次数；0 用默认 3
	BaseBackoff     time.Duration     // 指数退避基数；0 用默认 100ms
}

// defaultOptions 返回内置默认配置。
func defaultOptions() Options {
	return Options{Interval: defaultInterval, MaxRetries: defaultMaxRetries, BaseBackoff: defaultBaseBackoff, Routes: map[string]string{}}
}

// MQ 异步消息组件（hotswap 独立组件，Name = "mq"，不挂 chain）。
type MQ struct {
	mu        sync.Mutex
	db        *sql.DB
	table     string
	sqls      db.SQLSource // SQL 脚本源（装配时注入 internal/db）
	store     *OutboxStore
	deliverer *PollingDeliverer
	options   *Options // 装配期注入的运行参数（SetOptions），Start 时合并
	state     atomic.Value
}

// 编译期断言：MQ 实现 hotswap.Component。
var _ hotswap.Component = (*MQ)(nil)

// New 构造 MQ 组件（初始 StateDisabled，由 hotswap.Enable 触发 Start）。
// 使用前需调用 SetSQLSource 注入 SQL 脚本源（SQL 外置到 sql/<dbtype>/ 目录）。
func New(db *sql.DB, tableName string) *MQ {
	m := &MQ{db: db, table: tableName}
	m.state.Store(hotswap.StateDisabled)
	return m
}

// SetSQLSource 注入 SQL 脚本源（通常为 internal/db 数据访问层）。
func (m *MQ) SetSQLSource(src db.SQLSource) {
	m.sqls = src
}

// SetOptions 注入运行参数（轮询间隔/消费方地址/重试次数/退避基数），Start 时生效。
// 与 Start(cfg) 的 Options 优先级：本方法注入的 options 为装配期默认，Start(cfg) 非 nil 时覆盖。
func (m *MQ) SetOptions(opts Options) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.options = &opts
}

// Name 返回组件名：mq。
func (m *MQ) Name() string { return "mq" }

// Start 初始化 outbox 表并启动投递器；cfg 可为 nil 或 *Options。幂等。
func (m *MQ) Start(cfg any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deliverer != nil {
		return nil // 幂等：已启动
	}
	if m.sqls == nil {
		return fmt.Errorf("mq: 未注入 SQL 脚本源（SetSQLSource），无法加载 sql/<dbtype>/ 下脚本")
	}

	opts := defaultOptions()
	if m.options != nil {
		opts = *m.options
	}
	if cfg != nil {
		o, ok := cfg.(*Options)
		if !ok {
			return fmt.Errorf("mq: 不支持的启动配置类型 %T，需 *mq.Options", cfg)
		}
		opts = *o
	}

	store := NewOutboxStore(m.db, m.table)
	store.SetSQLSource(m.sqls)
	store.SetMaxRetries(opts.MaxRetries)
	if err := store.EnsureTable(); err != nil {
		return fmt.Errorf("mq: 初始化 outbox 表失败: %w", err)
	}

	d := NewPollingDeliverer(store, opts.Interval)
	d.SetBaseBackoff(opts.BaseBackoff)
	d.SetMaxRetries(opts.MaxRetries)
	d.SetConsumerBaseURL(opts.ConsumerBaseURL)
	for topic, u := range opts.Routes {
		d.SetRoute(topic, u)
	}
	if err := d.Start(); err != nil {
		return err
	}
	m.store = store
	m.deliverer = d
	m.state.Store(hotswap.StateEnabled)
	return nil
}

// Stop 停止投递器并置 Disabled。幂等。
func (m *MQ) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deliverer == nil {
		return nil // 幂等
	}
	if err := m.deliverer.Stop(); err != nil {
		return err
	}
	m.deliverer = nil
	m.store = nil
	m.state.Store(hotswap.StateDisabled)
	return nil
}

// State 返回组件自身状态。
func (m *MQ) State() hotswap.State {
	return m.state.Load().(hotswap.State)
}

// Store 返回当前 OutboxStore（供业务方在启动后插入消息）；组件未启动返回 nil。
func (m *MQ) Store() *OutboxStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store
}
