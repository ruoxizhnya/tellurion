package mining

// SVC-CORE-DATA-RETIRE（2026-11-08）：本文件是原 `pkg/data/factor_governance.go`
// 的迁移落点。数据面（connector / ETL / 因子计算）整体退役后，挖掘流水线仍
// 需要一套进程内的登记账本做治理留痕，但它不再属于「数据层」——随唯一使用者
// pkg/mining 留在本包，公式闸门见同包 registry.go（代码型内核的 Go 出口
// 随因子计算一并退役，登记只校验结构/公式，不再绑定 FactorComputer）。

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// ErrRegistrationDuplicate：同一 (因子名, 版本哈希) 重复登记——改定义必须
// 改哈希（版本纪律），原样重放同一版是幂等而非新增。
var ErrRegistrationDuplicate = fmt.Errorf("factor version already registered")

// RegistrationBook 是因子登记的进程内账本。
//
// 并发安全（REV-03）：它是**可变**账本——Register/Rollback 直接改
// byNameHash / ordered / audit，故自持 mu 串行化写侧、保护读侧。
//
// 注：因含 mu，本类型**不可按值拷贝**（拷贝会复制锁）；一律以 *RegistrationBook
// 传递——构造入口 NewRegistrationBook / NewRegistrationBookFrom 均返回指针。
type RegistrationBook struct {
	mu sync.RWMutex
	// byNameHash 按 (因子名, 版本哈希) 索引登记记录。
	byNameHash map[domain.FactorType]map[string]*domain.FactorRegistration
	// ordered 保持登记顺序（判定「最新在册版本」时同刻按登记序取后者，
	// 避免 map 遍历的随机性破坏确定性）。
	ordered []*domain.FactorRegistration
	audit   []domain.RegistrationAuditEntry
}

// NewRegistrationBook 构造空账本。
func NewRegistrationBook() *RegistrationBook {
	return &RegistrationBook{
		byNameHash: make(map[domain.FactorType]map[string]*domain.FactorRegistration),
	}
}

// NewRegistrationBookFrom 用一批已存在的登记（如从库回读）重建账本，
// 逐条过四样校验 + 去重（fail-closed）。不重跑注册表表达式闸门——与
// LoadFactorDefinitions 同款：加载只校验「结构与指纹自洽」。
func NewRegistrationBookFrom(regs []domain.FactorRegistration) (*RegistrationBook, error) {
	b := NewRegistrationBook()
	for _, reg := range regs {
		r := reg
		if err := r.Validate(); err != nil {
			return nil, err
		}
		byHash := b.byNameHash[r.Definition.Name]
		if byHash == nil {
			byHash = make(map[string]*domain.FactorRegistration)
			b.byNameHash[r.Definition.Name] = byHash
		}
		if _, dup := byHash[r.VersionHash]; dup {
			return nil, fmt.Errorf("%w: %s@%s", ErrRegistrationDuplicate, r.Definition.Name, r.VersionHash)
		}
		byHash[r.VersionHash] = &r
		b.ordered = append(b.ordered, &r)
	}
	return b, nil
}

// Register 登记一版定义（治理写入口）。
//
// 顺序即纪律：先过四样校验（domain.FactorRegistration.Validate），再过
// 注册表的表达式闸门（公式可解析 / 过 L1 / identifier ↔ 输入字段双向
// 相等 / 输入字段 ⊆ 规范层字段词表）。登记不是「写个字符串进库」，是
// 「这一版定义真的能算」——与注册表判据同源。
func (b *RegistrationBook) Register(def domain.FactorDefinition, registrar, reason string, at time.Time) (*domain.FactorRegistration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	reg := &domain.FactorRegistration{
		Definition:   def,
		VersionHash:  def.VersionHash(),
		Registrar:    registrar,
		Status:       domain.RegistrationActive,
		RegisteredAt: at,
	}
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	if _, err := NewRegistry([]domain.FactorDefinition{def}); err != nil {
		return nil, fmt.Errorf("factor %q: registration failed registry gates: %w", def.Name, err)
	}
	byHash := b.byNameHash[def.Name]
	if byHash == nil {
		byHash = make(map[string]*domain.FactorRegistration)
		b.byNameHash[def.Name] = byHash
	}
	if _, dup := byHash[reg.VersionHash]; dup {
		return nil, fmt.Errorf("%w: %s@%s (a content change must change the version hash)", ErrRegistrationDuplicate, def.Name, reg.VersionHash)
	}
	byHash[reg.VersionHash] = reg
	b.ordered = append(b.ordered, reg)
	b.audit = append(b.audit, domain.RegistrationAuditEntry{
		Name:        def.Name,
		VersionHash: reg.VersionHash,
		Action:      domain.ActionRegister,
		Actor:       registrar,
		Reason:      reason,
		Detail:      fmt.Sprintf("kernel=%s caliber=%s effective_from=%s", def.Kernel, def.Caliber, def.EffectiveFrom),
		At:          at,
	})
	return reg, nil
}

// Rollback 回滚某一版登记：新引用不再命中，历史引用照旧命中。
//
// 找不到版本、已回滚、或缺 actor（审计「谁」）都 fail-closed 报错。
func (b *RegistrationBook) Rollback(name domain.FactorType, versionHash, actor, reason string, at time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	byHash := b.byNameHash[name]
	reg, ok := byHash[versionHash]
	if !ok {
		return fmt.Errorf("factor %q: version %s not registered; nothing to roll back", name, versionHash)
	}
	if reg.Status == domain.RegistrationRolledBack {
		return fmt.Errorf("factor %q: version %s already rolled back", name, versionHash)
	}
	if strings.TrimSpace(actor) == "" {
		return fmt.Errorf("factor %q: rollback actor missing (audit trail requires 'who')", name)
	}
	reg.Status = domain.RegistrationRolledBack
	reg.RolledBackAt = &at
	reg.RolledBackBy = actor
	reg.RollbackReason = reason
	b.audit = append(b.audit, domain.RegistrationAuditEntry{
		Name:        name,
		VersionHash: versionHash,
		Action:      domain.ActionRollback,
		Actor:       actor,
		Reason:      reason,
		Detail:      "new references will no longer resolve this version; pinned (historical) references still hit",
		At:          at,
	})
	return nil
}

// ResolveEffective 解析一个因子的「新引用」应命中哪一版：最新的**在册**
// （active）版本。被回滚的版本不参与。无在册版本 → ok=false。
func (b *RegistrationBook) ResolveEffective(name domain.FactorType) (domain.FactorDefinition, string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var best *domain.FactorRegistration
	for _, reg := range b.ordered {
		if reg.Definition.Name != name || reg.Status != domain.RegistrationActive {
			continue
		}
		// 登记序里越靠后的越新；同刻登记（RegisteredAt 相等）取后者。
		if best == nil || !reg.RegisteredAt.Before(best.RegisteredAt) {
			best = reg
		}
	}
	if best == nil {
		return domain.FactorDefinition{}, "", false
	}
	return best.Definition, best.VersionHash, true
}

// ResolvePinned 解析一个因子的「历史引用」：按版本哈希精确锁定，无论该
// 版本是否已回滚都能命中——历史回测钉着当时的哈希跑，回滚不许破坏复现。
func (b *RegistrationBook) ResolvePinned(name domain.FactorType, versionHash string) (domain.FactorDefinition, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	reg, ok := b.byNameHash[name][versionHash]
	if !ok {
		return domain.FactorDefinition{}, false
	}
	return reg.Definition, true
}

// Registrations 返回登记快照（登记序）。
func (b *RegistrationBook) Registrations() []domain.FactorRegistration {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]domain.FactorRegistration, 0, len(b.ordered))
	for _, reg := range b.ordered {
		out = append(out, *reg)
	}
	return out
}

// Audit 返回审计留痕（登记序）。
func (b *RegistrationBook) Audit() []domain.RegistrationAuditEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]domain.RegistrationAuditEntry, len(b.audit))
	copy(out, b.audit)
	return out
}
