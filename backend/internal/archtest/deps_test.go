package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modulePath 必须与 backend/go.mod 的 module 行一致。
// 改动 module 名时同步修改，否则所有规则都会因为匹配不到前缀而静默失效。
const modulePath = "github.com/vevila2020/order-sys"

// businessModules 是需要遵守分层规则的业务模块（需求 §9.1）。
// flashsale、notification、matching 尚未创建；在这里预先登记，使它们建立时自动
// 纳入检查——未创建的目录会被跳过，不会让测试失败。
var businessModules = []string{"order", "inventory", "payment", "flashsale", "notification", "matching"}

// businessLayers 是每个业务模块内的分层目录。
var businessLayers = []string{"domain", "app", "infra", "http"}

// frameworkPrefixes 是编排层不能直接依赖的框架与外部 I/O 客户端。
// 它们必须出现在 infra 适配器里，由 app 通过端口调用：
// 直接依赖 gorm 会让事务边界从 app 渗回用例，直接依赖 kafka/redis/asynq 则
// 会把外部 I/O 拖进事务（ADR-0003 第 6 节明确禁止）。
var frameworkPrefixes = []string{
	"github.com/gin-gonic/gin",
	"gorm.io/gorm",
	"gorm.io/driver",
	"github.com/spf13/viper",
	"github.com/segmentio/kafka-go",
	"github.com/redis/go-redis",
	"github.com/hibiken/asynq",
}

func TestBusinessLayering(t *testing.T) {
	checked := 0
	for _, m := range businessModules {
		for _, layer := range businessLayers {
			rel := filepath.Join(m, layer)
			pkg, ok := importPackage(t, rel)
			if !ok {
				continue
			}
			checked++

			// 包自身路径：外部测试包（package domain_test）必须 import 被测包，
			// 这不算违反"领域层只用标准库"。不豁免会产生一条无法通过、也无法
			// 合理关闭的误报——只能把测试降级为内部测试，白白失去黑盒视角。
			selfPath := modulePath + "/internal/" + m + "/" + layer

			for _, imp := range importsOf(pkg) {
				if imp == selfPath {
					continue
				}
				if reason, bad := layerRule(m, layer).violation(imp); bad {
					t.Errorf("internal/%s/%s 违规 import %q：%s", m, layer, imp, reason)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("没有检查到任何业务包：目录名或 modulePath 可能已变更")
	}
}

// TestPlatformDoesNotDependOnBusiness 断言 infrastructure 不反向依赖业务规则。
// platform 是所有模块共用的底座，一旦它 import 了 order，依赖图就会出现环，
// 且业务规则会以"平台能力"的名义扩散到各处。
func TestPlatformDoesNotDependOnBusiness(t *testing.T) {
	var forbidden []string
	for _, m := range businessModules {
		forbidden = append(forbidden, modulePath+"/internal/"+m)
	}

	platformRoot := filepath.Join("..", "platform")
	checked := 0
	err := filepath.WalkDir(platformRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel("..", path)
		if relErr != nil {
			return relErr
		}
		pkg, ok := importPackage(t, rel)
		if !ok {
			return nil
		}
		checked++
		for _, imp := range importsOf(pkg) {
			for _, f := range forbidden {
				if imp == f || strings.HasPrefix(imp, f+"/") {
					t.Errorf("internal/%s 违规 import %q：platform 不得依赖业务模块", filepath.ToSlash(rel), imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 platform 失败: %v", err)
	}
	if checked == 0 {
		t.Fatal("没有检查到任何 platform 包")
	}
}

// layerRule 返回某一层的 import 规则。规则按"该层需要知道什么"定义：
// 越往内，允许知道的越少。
func layerRule(module, layer string) rule {
	self := modulePath + "/internal/" + module

	// 其他模块的 infra/http 是各模块的实现细节。
	var otherModuleInternals []string
	for _, m := range businessModules {
		if m == module {
			continue
		}
		otherModuleInternals = append(otherModuleInternals,
			modulePath+"/internal/"+m+"/infra",
			modulePath+"/internal/"+m+"/http",
		)
	}

	switch layer {
	case "domain":
		// 纯领域模型：只允许标准库。连本模块的 app/infra 也不行，
		// 否则领域层会反向依赖编排层。
		return rule{stdlibOnly: true}

	case "app":
		// 编排层：认识本模块的 domain 与其他模块的 app 端口，
		// 但不认识任何持久化实现和 HTTP 细节，也不直接持有外部 I/O 客户端。
		forbidden := append([]string{self + "/infra", self + "/http"}, otherModuleInternals...)
		return rule{forbiddenPrefixes: append(forbidden, frameworkPrefixes...)}

	case "http":
		// handler 只绑定参数、调用 app、映射响应；直接拿 *gorm.DB 就会绕过用例。
		return rule{forbiddenPrefixes: []string{
			self + "/infra",
			"gorm.io/gorm",
		}}

	default: // infra
		// 适配器可以实现任何层的端口，但不能 import 其他模块的 http。
		var forbidden []string
		for _, m := range businessModules {
			if m == module {
				continue
			}
			forbidden = append(forbidden, modulePath+"/internal/"+m+"/http")
		}
		return rule{forbiddenPrefixes: forbidden}
	}
}

type rule struct {
	// stdlibOnly 表示该层只能使用标准库。
	stdlibOnly bool
	// forbiddenPrefixes 是该层绝对不能出现的 import 路径前缀（按路径段匹配）。
	forbiddenPrefixes []string
}

// violation 返回违规原因；第二返回值为 false 表示该 import 合法。
func (r rule) violation(imp string) (string, bool) {
	if isStdlib(imp) {
		return "", false
	}
	if r.stdlibOnly {
		return "该层只允许标准库，框架与内部包都必须通过端口注入", true
	}
	for _, p := range r.forbiddenPrefixes {
		// 按路径段比较：防止 "internal/platform/db" 误伤 "internal/platform/dbx"。
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return "违反依赖方向（" + p + "）", true
		}
	}
	return "", false
}

// isStdlib 用 import 路径第一段是否含点号判断：标准库路径的第一段没有 "."，
// 第三方与内部模块路径则一定有（域名或模块路径）。
// 这是 Go 生态的惯用近似；极少数无点号的第三方包会被误判为标准库，
// 本项目不依赖这类包。
func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// TestIsStdlib 锁定 isStdlib 的判定边界。
// 这个函数是"领域层只允许标准库"规则的唯一判据，一旦它把内部包误判为标准库，
// 整组规则都会静默放行，因此单独测试。
func TestIsStdlib(t *testing.T) {
	markers := []string{"context", "errors", "encoding/json", "time", "database/sql"}
	for _, imp := range markers {
		if !isStdlib(imp) {
			t.Errorf("%q 应被判定为标准库", imp)
		}
	}

	nonStdlib := []string{
		"github.com/gin-gonic/gin",
		"gorm.io/gorm",
		"golang.org/x/sync/errgroup",
		modulePath + "/internal/order/domain",
	}
	for _, imp := range nonStdlib {
		if isStdlib(imp) {
			t.Errorf("%q 不应被判定为标准库", imp)
		}
	}
}

// importPackage 解析 internal/<rel> 目录。
// 目录不存在或没有 Go 文件时返回 ok=false 而不是失败——业务模块是按里程碑
// 逐步建立的，尚不存在的模块不应让规则测试变红。
func importPackage(t *testing.T, rel string) (*build.Package, bool) {
	t.Helper()

	dir := filepath.Join("..", rel)
	if _, err := os.Stat(dir); err != nil {
		return nil, false
	}
	pkg, err := build.ImportDir(dir, 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			return nil, false
		}
		t.Fatalf("解析目录 internal/%s 失败: %v", filepath.ToSlash(rel), err)
	}
	return pkg, true
}

// importsOf 汇总普通 import、测试 import 与外部测试包 import。
// 测试里的违规同样会扩散到实现，所以一并检查。
func importsOf(pkg *build.Package) []string {
	out := make([]string, 0, len(pkg.Imports)+len(pkg.TestImports)+len(pkg.XTestImports))
	out = append(out, pkg.Imports...)
	out = append(out, pkg.TestImports...)
	out = append(out, pkg.XTestImports...)
	return out
}
