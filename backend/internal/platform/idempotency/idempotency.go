package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/platform/db"
)

// 本包对外的哨兵错误。
//
// 语义划分的依据是"客户端该做什么"，而不是"哪里出错"：
//   - ErrKeyReused：同键异体，客户端换了请求内容，必须换键；
//   - ErrInProgress：同键同体仍在处理，客户端应退避后重试；
//   - ErrInvalidKey / ErrInvalidRequest：请求本身不合法，重试无用。
//
// 平台包不决定 HTTP 状态码，由 app 层映射（ADR-0006 第 4 节）。
var (
	ErrInvalidKey     = errors.New("invalid idempotency key")
	ErrInvalidRequest = errors.New("invalid request body")
	ErrKeyReused      = errors.New("idempotency key reused with different request")
	ErrInProgress     = errors.New("idempotency key is being processed")
)

// Status 是幂等记录的状态。数据库中只会有这两种取值（迁移文件同上）。
type Status string

const (
	// StatusInProgress 只存在于未提交的事务里：提交前记录不可见，回滚时一并消失。
	StatusInProgress Status = "IN_PROGRESS"
	// StatusCompleted 表示用例已成功执行，response_snapshot 可重放。
	StatusCompleted Status = "COMPLETED"
)

// Record 是一条幂等记录。
type Record struct {
	Scope       string
	UserID      string
	Key         string
	RequestHash string
	Status      Status
	// Snapshot 是成功响应的 data 部分；仅在 StatusCompleted 时有意义。
	Snapshot []byte
}

// 列名常量化：表名与列名由迁移文件固定（snake_case），集中在这里便于评审时
// 一处核对。idem_key 而不是 key：key 是 MySQL 保留字。
const (
	tableName = "idempotency_records"
	colScope  = "scope"
	colUserID = "user_id"
	colKey    = "idem_key"
)

// Store 在调用方的事务内读写幂等记录。
//
// 所有方法都接收 tx 而不是自行开事务：幂等记录必须与业务写入原子提交
// （ADR-0006 第 3 节）。传入连接池上的 *gorm.DB 会跑在事务之外，回滚时留下
// 永远处理中的键——这正是 ADR 拒绝"独立短事务"方案的原因。
//
// 与 TransactionManager 一样，tx 是 *gorm.DB；实现内部对它执行 SQL，调用方
// 只负责把句柄传进来，不得自己拼装幂等表的 SQL。
type Store interface {
	// Begin 占用幂等键。三种结局：
	//   - 返回 (nil, nil)：键由本次调用占用，调用方可继续执行业务写入；
	//   - 返回 (existing, nil)：同键同体且已完成，existing.Status == StatusCompleted，
	//     调用方应重放 existing.Snapshot；
	//   - 返回错误：ErrKeyReused（指纹不同）、ErrInProgress（另有请求持有该键）
	//     或其他数据库错误（原样返回，交给 TransactionManager 决定是否重试）。
	Begin(ctx context.Context, tx *gorm.DB, rec Record) (*Record, error)

	// Complete 把记录标记为 COMPLETED 并写入快照。
	//
	// 影响行数不为 1 时返回内部错误并让事务回滚：记录被并发方改写属于不应发生的
	// 状态，宁可失败也不能写出与订单不一致的快照（ADR-0006 第 3 节）。
	Complete(ctx context.Context, tx *gorm.DB, rec Record, snapshot []byte) error
}

// NewStore 构造基于 MySQL 的幂等记录存储。
// ttl 必须为正；调用方（main）从配置注入，校验在 configx 启动期已完成。
func NewStore(ttl time.Duration) Store {
	return &mysqlStore{ttl: ttl}
}

type mysqlStore struct {
	ttl time.Duration
}

// Begin 实现 Store.Begin。
//
// 并发语义完全交给唯一索引 uk_idempotency (scope, user_id, idem_key)：
// 后到者的 INSERT 会等待先到事务的隐式锁。先到提交 → 本调用收到 1062 并读取
// 已提交记录；先到回滚 → 本调用 INSERT 成功，自己完整执行用例。应用层不做
// "先查后写"，那在并发下必然失效（ADR-0003 背景、ADR-0006 第 4 节）。
func (s *mysqlStore) Begin(ctx context.Context, tx *gorm.DB, rec Record) (*Record, error) {
	if err := validateRecord(rec); err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errors.New("idempotency: tx 不能为空")
	}
	if s.ttl <= 0 {
		return nil, errors.New("idempotency: ttl 必须为正")
	}

	// expires_at 用数据库时钟计算：应用与数据库的时区/时钟偏差会让保留期
	// 与 now() 比较产生无法解释的漂移。UTC_TIMESTAMP(6) 与迁移里的 DATETIME(6)
	// 精度一致，避免毫秒被四舍五入后出现"刚插入就过期"的假象。
	//
	// 用 Exec 而不是 Create：本包不该为一张平台表维护 GORM 模型，显式 SQL 也让
	// 影响行数（这里是错误判定）和时间函数都一目了然。
	err := tx.WithContext(ctx).Exec(
		`INSERT INTO `+tableName+`
		        (`+colScope+`, `+colUserID+`, `+colKey+`, request_hash, status, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6) + INTERVAL ? MICROSECOND, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		rec.Scope, rec.UserID, rec.Key, rec.RequestHash, string(StatusInProgress), s.ttl.Microseconds(),
	).Error
	if err == nil {
		return nil, nil
	}

	// 只有唯一约束冲突才是"键已被占用"。其他错误（连接断开、语法问题）原样返回，
	// 由调用方的事务管理器判断是否重试。
	if !db.IsDuplicateKey(err) {
		return nil, normalizeKeyError(ctx, err)
	}
	return s.readExisting(ctx, tx, rec)
}

// readExisting 在 1062 之后读取既有记录，并按指纹分支。
func (s *mysqlStore) readExisting(ctx context.Context, tx *gorm.DB, rec Record) (*Record, error) {
	existing, err := s.load(ctx, tx, rec)
	if err != nil {
		return nil, err
	}
	// 指纹不同表示客户端换了请求内容却复用了键：必须拒绝，否则会得到与
	// 首次请求无关的业务结果（ADR-0006 第 2 节判定顺序）。
	if existing.RequestHash != rec.RequestHash {
		return nil, fmt.Errorf("%w: scope=%s", ErrKeyReused, rec.Scope)
	}
	// 指纹相同但状态不是 COMPLETED：另一种可能的源头是"同键并发提交"时读到
	// 了未提交行——RR 隔离下不会发生，因此这里只可能是实现或数据被外部改写。
	// 按"处理中"返回仍然安全：客户端退避重试，最终会拿到 COMPLETED 快照。
	if existing.Status != StatusCompleted {
		return nil, &inProgressError{cause: fmt.Errorf("记录状态为 %s", existing.Status)}
	}
	return existing, nil
}

// load 读取一条记录；不存在时返回明确的内部错误。
func (s *mysqlStore) load(ctx context.Context, tx *gorm.DB, rec Record) (*Record, error) {
	// 列名必须显式标注：GORM 的默认命名策略会把 Snapshot 映射到 snapshot、
	// Key 映射到 key，与迁移里的 response_snapshot / idem_key 对不上，结果是
	// 字段静默保持零值——重放时快照为空，比报错更难发现。
	var row struct {
		Scope       string `gorm:"column:scope"`
		UserID      string `gorm:"column:user_id"`
		Key         string `gorm:"column:idem_key"`
		RequestHash string `gorm:"column:request_hash"`
		Status      string `gorm:"column:status"`
		Snapshot    []byte `gorm:"column:response_snapshot"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT scope, user_id, idem_key, request_hash, status, response_snapshot
		       FROM `+tableName+`
		      WHERE scope = ? AND user_id = ? AND idem_key = ?`,
			rec.Scope, rec.UserID, rec.Key).
		Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("idempotency: 读取既有记录: %w", err)
	}
	// Scan 在无行时不报错，需要单独判断：拿到 1062 之后记录却不见了，
	// 只可能是记录被并发删除（保留清理任务）。此时不能当作"原结果"重放。
	if row.Scope == "" {
		return nil, errors.New("idempotency: 唯一索引冲突但记录不存在")
	}
	return &Record{
		Scope:       row.Scope,
		UserID:      row.UserID,
		Key:         row.Key,
		RequestHash: row.RequestHash,
		Status:      Status(row.Status),
		Snapshot:    row.Snapshot,
	}, nil
}

// Complete 实现 Store.Complete。
func (s *mysqlStore) Complete(ctx context.Context, tx *gorm.DB, rec Record, snapshot []byte) error {
	if tx == nil {
		return errors.New("idempotency: tx 不能为空")
	}
	if len(snapshot) == 0 {
		// 空快照会让重放返回空 data，看起来像"订单消失了"。空响应也必须
		// 显式写成 `null`，由调用方决定序列化形式。
		return errors.New("idempotency: snapshot 不能为空")
	}
	// 不必校验 JSON 合法性：快照由本服务的响应编码产生，此处再做一次解析
	// 只会增加一条可能失败的路径。MySQL 的 JSON 列会拒绝非法文档，已是最后防线。
	//
	// 注意 JSON 列会规范化内容（键排序、冒号后补空格），读回的值与写入原文
	// 不一定逐字节相同。重放语义是"客户端解析得到相同数据"，因此不要在调用方
	// 对快照做字节比较或计算外层哈希。

	result := tx.WithContext(ctx).Exec(
		`UPDATE `+tableName+`
		    SET status = ?, response_snapshot = ?, updated_at = UTC_TIMESTAMP(6)
		  WHERE scope = ? AND user_id = ? AND idem_key = ? AND status = ?`,
		string(StatusCompleted), string(snapshot),
		rec.Scope, rec.UserID, rec.Key, string(StatusInProgress),
	)
	if result.Error != nil {
		return fmt.Errorf("idempotency: 写入快照: %w", result.Error)
	}
	// 0 行表示记录已被并发方改写或删除。ADR-0006 第 3 节要求此时回滚整个事务：
	// 快照与业务数据必须同时成立，宁可失败也不能写出不一致的组合。
	if result.RowsAffected != 1 {
		return fmt.Errorf("idempotency: 标记完成时影响 %d 行（期望 1），记录状态已被并发改写", result.RowsAffected)
	}
	return nil
}

// validateRecord 校验作用域字段，避免把空值写进唯一索引。
//
// 唯一索引允许 NULL，但这里的列都是 NOT NULL：空字符串会占住
// (scope, ”, key) 这一组合，把不同用户的请求误判为同一个键。
func validateRecord(rec Record) error {
	if rec.Scope == "" {
		return errors.New("idempotency: scope 不能为空")
	}
	if rec.UserID == "" {
		return errors.New("idempotency: user_id 不能为空")
	}
	if rec.Key == "" {
		return errors.New("idempotency: key 不能为空")
	}
	if rec.RequestHash == "" {
		return errors.New("idempotency: request_hash 不能为空")
	}
	return nil
}

// normalizeKeyError 把"等锁超时"翻译为 ErrInProgress，其余错误原样返回。
//
// 这是 ADR-0006 第 4 节与 ADR-0003 第 5 节之间的唯一例外：幂等键 INSERT 上的
// 1205 表示另一个请求正在处理同一个键。若交给事务级重试，只会再次排到同一个
// 队尾，白白耗掉预算。context 超时（等待被中断）同理——它说明"等太久"，
// 而不是"我们自己的处理超时"。
//
// 注意只包装 INSERT 的错误：事务中途其他语句的超时仍然是 TIMEOUT / 可重试，
// 语义不同，不能一并改写。
func normalizeKeyError(ctx context.Context, err error) error {
	if db.IsLockWaitTimeout(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &inProgressError{cause: err}
	}
	// 真正的数据库故障原样返回，让 TransactionManager 的 retryAllowed 判定它
	// 是否属于可重试的瞬时争用（其他语句上的 1205/1213 仍走整事务重试）。
	return err
}

// inProgressError 是 ErrInProgress 的具体错误，并显式否决事务级重试。
//
// 为什么需要这个类型而不是直接 fmt.Errorf：错误链里同时有 1205 和
// context.DeadlineExceeded，TransactionManager 会把它们当作可重试的瞬时争用，
// 于是重试整个事务、再次排到同一个键的队尾，把预算耗光后仍返回
// DEPENDENCY_UNAVAILABLE——客户端拿到的是一个错误的结论。
// db 包用 retryVetoer 接口接收否决权（见 platform/db/errors.go），本类型实现它。
type inProgressError struct {
	cause error
}

func (e *inProgressError) Error() string {
	return fmt.Sprintf("%s: %v", ErrInProgress, e.cause)
}

// Unwrap 返回哨兵错误，使 errors.Is(err, ErrInProgress) 成立；
// 原始原因在 Error() 里保留，供日志排查，不参与 Is/As 判定。
func (e *inProgressError) Unwrap() error { return ErrInProgress }

// VetoRetry 实现 db.retryVetoer：等待同一个键不是瞬时争用，重试没有收益。
func (e *inProgressError) VetoRetry() bool { return true }

// Snapshot 把响应数据编码成可写入 JSON 列的快照。
//
// 独立成函数是为了让调用方不必关心 JSON 列的写入形式；同时也是"只存 data
// 部分"这条约定的落点——外层信封（request_id）必须由调用方排除。
func Snapshot(data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("idempotency: 编码快照: %w", err)
	}
	return raw, nil
}
