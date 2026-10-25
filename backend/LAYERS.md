# 模块分层与新模块开发指南

本文说明业务模块内 `domain` / `app` / `infra` / `http` 四个目录的职责与边界，
并给出新增模块（以 `user` 为例）的落地步骤。规则由 `internal/archtest` 强制，
本文解释**为什么**这样分层以及**怎么开始写**。

## 一、四个目录各管什么

| 目录 | 一句话 | 允许 import | 禁止 import |
|---|---|---|---|
| `domain` | **业务规则本身**：数据 + 不变量 + 状态机。不碰数据库、不碰 HTTP、不知道"接口"存在 | 只用标准库 | 本模块 app/http/infra、任何三方库 |
| `app` | **编排**：一次用例 = 一次事务，按顺序调用端口；定义端口接口 | 本模块 domain、其他模块 `app`、platform 的纯能力 | gorm、gin、本模块 infra/http |
| `infra` | **适配器**：把接口变成真实的 MySQL/Kafka/跨模块调用 | 任何东西 | 其他模块的 http |
| `http` | **翻译**：JSON ↔ 用例输入输出、错误码映射、挂路由 | 本模块 app、platform | 本模块 infra、gorm |

判断一句话就能定：

- **"这条规则不依赖任何外部系统也成立吗？"** → 是就进 `domain`（比如"金额不能为负"）
- **"这段代码在换数据库后要重写吗？"** → 是就进 `infra`
- **"这段代码在换 HTTP 框架后要重写吗？"** → 是就进 `http`
- **剩下的协调逻辑** → `app`

关键约束（`archtest` 强制）：依赖只能向内，`http → app → domain`，`infra → app`。

`app` 端口签名里的 `tx` 是 `db.Tx`，它是 `*gorm.DB` 的**类型别名**而不是接口——
这是一处已知、记录在案的抽象泄漏：仓储需要条件更新与 `RowsAffected`，再包一层
自定义 `Tx` 会把 gorm 的能力抄一遍。代价是靠评审维持"`app` 只能把 `tx` 原样透传给
端口，不得调用 ORM 方法"这条纪律，详见 `internal/platform/db/tx.go` 中
`TransactionManager` 的文档注释。

## 二、自己写一个模块，先干什么

**先别建文件夹。** 顺序是：

### 第 0 步：写清楚三件事（纸上）

1. **端点清单**：`POST /users`、`GET /users/:id`、`POST /users/login`…
2. **数据表**：字段、唯一索引、状态字段
3. **不变量**：哪些规则永远成立（邮箱格式、密码长度、一个邮箱只能注册一次）

这三样决定了后面每个文件写什么。

### 第 1 步：`domain` —— 从不变量开始

先写构造函数，因为**构造函数就是不变量的落点**：

```go
// internal/user/domain/user.go
func NewUser(email, passwordHash string) (*User, error)          // 校验并创建
func RehydrateUser(id, email, hash string, ...) (*User, error)   // 从库里还原，只做基本校验
```

项目里 `order` 就是这么做的：`NewOrder` 校验明细非空、`NewOrderItem` 校验数量价格。
**构造函数返回 error，而不是让不合法对象存在。**

### 第 2 步：`app/ports.go` —— 倒着定义接口

只看用例需要什么，不看 GORM 能干什么：

```go
type UserRepository interface {
	Create(ctx context.Context, tx db.Tx, u *domain.User) error
	GetByID(ctx context.Context, tx db.Tx, userID string) (*domain.User, error)
	GetByEmail(ctx context.Context, tx db.Tx, email string) (*domain.User, error)
}
```

### 第 3 步：`app` 用例 + `infra` 仓储

用例先写（它会告诉你接口够不够），仓储后写。仓储无状态、`tx` 按调用传入、
构造函数返回接口：

```go
func NewUserRepository() app.UserRepository { return userRepo{} }
```

### 第 4 步：`http`

`dto.go`（请求/响应结构 + binding tag）→ handler → `routes.go` → `errors.go`
（错误码映射）。

### 第 5 步：装配

`cmd/api/main.go` 里三行：

```go
userSvc := userapp.NewService(txManager, userinfra.NewUserRepository())
userHandler := userhttp.NewHandler(userSvc)
// RegisterRoutes 里：
userhttp.Register(g.Operator, userHandler)
```

## 三、user 模块的具体文件规划

```
internal/user/
├── domain/
│   ├── doc.go                 // 包注释：本包的职责与约束
│   ├── user.go                // User 聚合：不导出字段、NewUser/RehydrateUser、行为方法
│   ├── email.go               // 值对象 Email：构造即校验，内部已标准化（小写/去空格）
│   ├── password.go            // 值对象 PasswordHash：只管哈希串，不碰明文
│   ├── errors.go              // 领域哨兵：ErrInvalidEmail / ErrInvalidUserID ...
│   └── user_test.go
├── app/
│   ├── doc.go
│   ├── ports.go               // UserRepository 接口
│   ├── register.go            // RegisterUser 用例 + RegisterUserInput + UserView
│   ├── login.go               // Authenticate 用例
│   ├── errors.go              // 编排层哨兵：ErrEmailTaken / ErrInvalidCredentials
│   └── register_test.go       // 用假仓储，不连库
├── infra/
│   ├── doc.go
│   ├── repository.go          // GORM 实现 + 错误翻译（唯一索引 → ErrEmailTaken）
│   ├── repository_integration_test.go
│   └── // 文件末尾：var _ app.UserRepository = userRepo{}
└── http/
    ├── doc.go
    ├── dto.go                 // RegisterRequest/LoginRequest/UserResponse（json + binding tag）
    ├── register.go            // Handler.Register
    ├── login.go               // Handler.Login
    ├── errors.go              // toAppError：app/domain 错误 → httpx.Code
    ├── routes.go              // Register(group, h)
    └── handler_integration_test.go
```

### 几个容易踩的点

**① 密码必须在 `http` 层以外哈希吗？**

不是。哈希是业务规则（用什么算法、salt 怎么存），放 `app` 或一个 `domain` 的值对象
都行；但**绝不能放 `http`**——那样换登录入口就要重写一遍。`platform/authx` 已有
JWT 签发，注册用例产出的 `*domain.User` 交给它。

**② 唯一索引冲突谁来翻译？**

`infra` 收到 MySQL 1062，翻成 `app.ErrEmailTaken`（参考
`order/infra/adapters.go` 的 `translateInventoryError`）。`http` 只
`errors.Is(err, app.ErrEmailTaken)` → 409。**三层的错误集各不相同，翻译点固定在边界。**

**③ 别忘了登记到 archtest**

`internal/archtest/deps_test.go` 里：

```go
var businessModules = []string{"order", "inventory", "payment", "flashsale", "notification", "matching"}
```

`user` 不加进去，**分层规则对它完全不生效**，写得再乱也不会报错。这是最容易漏的一步。

**④ 空目录不要建**

没有 `infra` 需求（例如纯计算模块），就别建 `infra/`。项目里每个包都有 `doc.go`，
是因为它们都有实际内容；空包只会误导人。

## 一句话总结

> **domain 定规则，app 定流程和接口，infra 接线，http 翻译。写新模块从"不变量"出发
> 写 domain，再用例推接口、接口推仓储、最后套 HTTP——不要反过来从建文件夹开始。**
