package mining

// SVC-CORE-DATA-RETIRE（2026-11-08）：原 `pkg/data/factor_registry.go` 的
// 迁移落点（仅保留登记闸门需要的那部分）。退役的是「因子计算实现」——
// code 型因子绑定的 FactorComputer 随 pkg/data 一并删除，因此这里不再有
// codeKernels / CodeKernel / ComputeFactorByName；登记时对 code 型定义只
// 校验 CodeRef 非空（结构自洽），不再要求 Go 出口已绑定。

import (
	"fmt"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/expression"
)

// Registry 是因子定义的进程内注册表（「库管理」的读路径）。
//
// 不可变快照：构造时定稿（含跨层校验），之后只读。
type Registry struct {
	defs  map[domain.FactorType]domain.FactorDefinition
	order []domain.FactorType
}

// NewRegistry 用种子定义构造注册表，并做全量 fail-closed 校验：
//
//  1. 每条定义过 domain 结构校验（Validate）；
//  2. leaf/formula 型公式必须可解析、过引擎 L1 闸门；
//  3. 公式里的 identifier 必须与 InputFields **互相等于**；
//  4. InputFields 必须落在表达引擎字段注册表内；
//  5. code 型 CodeRef 必须非空（Go 出口随因子计算实现退役，不再校验绑定）；
//  6. 全量名字去重。
//
// 任何一条不过即返回 error——注册表加载失败是启动级故障。
func NewRegistry(defs []domain.FactorDefinition) (*Registry, error) {
	r := &Registry{
		defs: make(map[domain.FactorType]domain.FactorDefinition, len(defs)),
	}
	for _, d := range defs {
		if _, dup := r.defs[d.Name]; dup {
			return nil, fmt.Errorf("factor %q: duplicate definition in registry", d.Name)
		}
		if err := d.Validate(); err != nil {
			return nil, err
		}
		switch d.Kernel {
		case domain.KernelLeaf, domain.KernelFormula:
			if err := r.validateFormulaDef(d); err != nil {
				return nil, err
			}
		case domain.KernelCode:
			if d.CodeRef == "" {
				return nil, fmt.Errorf("factor %q: code kernel has empty code_ref", d.Name)
			}
		case domain.KernelPending:
			// 结构校验已覆盖（无公式无出口无输入）。
		}
		r.defs[d.Name] = d
		r.order = append(r.order, d.Name)
	}
	return r, nil
}

// validateFormulaDef 做公式型定义的跨层校验（fail-closed：公式引用不存在的
// 输入字段 → 加载期报错，而不是求值期算出垃圾分）。
func (r *Registry) validateFormulaDef(d domain.FactorDefinition) error {
	expr, err := expression.NewParser().Parse(d.Formula)
	if err != nil {
		return fmt.Errorf("factor %q: formula does not parse: %w", d.Name, err)
	}
	if err := expr.Validate(); err != nil {
		return fmt.Errorf("factor %q: formula fails L1 gate: %w", d.Name, err)
	}
	// identifier 集合 vs InputFields：双向相等。
	used := make(map[string]bool)
	for _, id := range expression.ExtractInputs(expr.AST) {
		used[id] = true
	}
	declared := make(map[string]bool, len(d.InputFields))
	for _, f := range d.InputFields {
		declared[f] = true
	}
	for id := range used {
		if !declared[id] {
			return fmt.Errorf("factor %q: formula uses field %q not declared in input_fields", d.Name, id)
		}
	}
	for f := range declared {
		if !used[f] {
			return fmt.Errorf("factor %q: input field %q declared but formula never uses it", d.Name, f)
		}
	}
	// InputFields 必须在引擎字段注册表内。
	known := make(map[string]bool)
	for _, f := range expression.AvailableFields() {
		known[f] = true
	}
	for _, f := range d.InputFields {
		if !known[f] {
			return fmt.Errorf("factor %q: input field %q not in canonical field registry", d.Name, f)
		}
	}
	// Validity（域过滤）同 Formula 规则。
	if d.Validity != "" {
		vExpr, err := expression.NewParser().Parse(d.Validity)
		if err != nil {
			return fmt.Errorf("factor %q: validity does not parse: %w", d.Name, err)
		}
		if err := vExpr.Validate(); err != nil {
			return fmt.Errorf("factor %q: validity fails L1 gate: %w", d.Name, err)
		}
		for _, id := range expression.ExtractInputs(vExpr.AST) {
			if !declared[id] {
				return fmt.Errorf("factor %q: validity uses field %q not declared in input_fields", d.Name, id)
			}
		}
	}
	return nil
}

// Definition 取一个因子的定义。未登记 → ok=false。
func (r *Registry) Definition(name domain.FactorType) (domain.FactorDefinition, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Names 返回全部已登记因子名（定义序）。
func (r *Registry) Names() []domain.FactorType {
	out := make([]domain.FactorType, len(r.order))
	copy(out, r.order)
	return out
}

// VersionHash 返回某因子定义的版本指纹（未登记 → 空串）。
func (r *Registry) VersionHash(name domain.FactorType) string {
	d, ok := r.defs[name]
	if !ok {
		return ""
	}
	return d.VersionHash()
}
