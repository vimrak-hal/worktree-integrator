package config

import (
	"fmt"
	"slices"
)

// CopySpec は 1 つの copy テーブル（[defaults.copy] または [repos.<name>.copy]）の
// 生の設定である。`git worktree add` は追跡対象の内容しか実体化しないため、
// gitignore されたもの（.env、ローカルの秘密情報、データディレクトリなど）は新しい
// worktree には存在しない。CopySpec はユーザーが、明示的なパスのリスト、または
// 「gitignore されたすべて」（除外指定付き）のいずれかを選べるようにする。
//
// 実際にどのファイルをコピーするかは、defaults.copy と repos.<name>.copy を
// マージした CopyPlan（copyplan.go 相当、本ファイル内）が決める。コピーの実行そのもの
// （エンジン）は infra/fscopy に分離されている。
type CopySpec struct {
	// Paths はコピーする明示的な相対パス（常にコピーされ、決して除外されない）。
	Paths []string
	// Gitignored は gitignore されたすべてのエントリ（Exclude を除く）のコピーを
	// 要求する。
	Gitignored bool
	// Exclude は gitignored モードのための追加の除外パターン（gitignore 互換、
	// infra/fscopy が解釈する）。defaults と repo の両方のエントリがあれば和集合になる。
	Exclude []string
	// ExcludeDefaults は組み込みの既定除外（builtinExcludeDefaults）を適用するかどうか。
	// nil（省略）は true（適用する）と同義。false を明示すると、そのレベルではオプト
	// アウトする。defaults と repo の両方に書かれた場合はより具体的な repo 側が勝つ
	// （MergeCopy を参照）。
	ExcludeDefaults *bool
}

// builtinExcludeDefaults は gitignored=true のときに組み込みで適用される既定の
// 除外パターンである。node_modules・.venv 等の巨大または再生成可能なディレクトリを
// 誤って worktree ごとに複製してしまう危険側デフォルトを避けるためのものであり、
// [repos.<name>.copy] / [defaults.copy] の exclude_defaults = false でオプトアウト
// できる。
var builtinExcludeDefaults = []string{
	"node_modules",
	".venv",
	"venv",
	"target",
	".direnv",
	".cache",
	".DS_Store",
}

// UnmarshalTOML は、相対パスの配列（paths の速記形）またはテーブル
// （paths/gitignored/exclude/exclude_defaults）のいずれかをデコードする。テーブル
// 形式は未知のキーを拒否するため、タイプミスは黙って無視されるのではなく報告される。
func (c *CopySpec) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case []any:
		paths, err := stringSlice("copy paths", t)
		if err != nil {
			return err
		}
		c.Paths = paths
	case map[string]any:
		for k := range t {
			switch k {
			case "paths", "gitignored", "exclude", "exclude_defaults":
			default:
				return fmt.Errorf("copy エントリに未知のキー %q があります", k)
			}
		}
		if raw, ok := t["paths"]; ok {
			arr, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("copy の `paths` は文字列の配列である必要があります（%T が指定されました）", raw)
			}
			paths, err := stringSlice("copy paths", arr)
			if err != nil {
				return err
			}
			c.Paths = paths
		}
		if raw, ok := t["gitignored"]; ok {
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("copy の `gitignored` は真偽値である必要があります（%T が指定されました）", raw)
			}
			c.Gitignored = b
		}
		if raw, ok := t["exclude"]; ok {
			arr, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("copy の `exclude` は文字列の配列である必要があります（%T が指定されました）", raw)
			}
			ex, err := stringSlice("copy exclude", arr)
			if err != nil {
				return err
			}
			c.Exclude = ex
		}
		if raw, ok := t["exclude_defaults"]; ok {
			b, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("copy の `exclude_defaults` は真偽値である必要があります（%T が指定されました）", raw)
			}
			c.ExcludeDefaults = &b
		}
	default:
		return fmt.Errorf("copy エントリは配列またはテーブルである必要があります（%T が指定されました）", v)
	}
	return nil
}

func stringSlice(what string, arr []any) ([]string, error) {
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("%s は文字列である必要があります（%T が指定されました）", what, e)
		}
		out = append(out, s)
	}
	return out, nil
}

// excludeDefaults は、このレベルで組み込み既定除外を適用すべきかを返す
// （省略時は true）。
func (c CopySpec) excludeDefaults() bool {
	return c.ExcludeDefaults == nil || *c.ExcludeDefaults
}

// Validate はこの copy 設定が構造的に妥当かを検証する: paths / exclude の各要素は
// 空文字列であってはならない（空エントリは何も指定していないのと同じであり、ほぼ
// 確実に設定ミスである）。
func (c CopySpec) Validate() error {
	for _, p := range c.Paths {
		if p == "" {
			return fmt.Errorf("`paths` に空文字列を含めることはできません")
		}
	}
	for _, ex := range c.Exclude {
		if ex == "" {
			return fmt.Errorf("`exclude` に空文字列を含めることはできません")
		}
	}
	return nil
}

// CopyPlan は、defaults.copy と repos.<name>.copy をマージした、単一リポジトリ向けの
// 解決済みコピー計画である。Exclude は組み込みの既定除外（オプトアウトされていなければ）
// を含む最終形であり、infra/fscopy.CopyInto へそのまま渡せる。
type CopyPlan struct {
	// Paths はコピーする明示的な相対パス（常にコピーされ、決して除外されない）。
	Paths []string
	// Gitignored は gitignore されたすべてのエントリ（Exclude を除く）のコピーを
	// 要求する。
	Gitignored bool
	// Exclude は gitignored モードのための最終的な除外パターン（gitignore 互換）。
	// 組み込みの既定除外を含む（exclude_defaults=false でオプトアウトされていない
	// 限り）。
	Exclude []string
}

// IsEmpty はこの計画が何も要求していないかを返す。
func (p CopyPlan) IsEmpty() bool { return len(p.Paths) == 0 && !p.Gitignored }

// MergeCopy は defaults（[defaults.copy]）と repo（[repos.<name>.copy]）をマージし、
// 単一リポジトリ向けの CopyPlan を解決する。明示的なパスは重複排除され順序が保たれる
// （defaults が先）。Gitignored は両者の OR。Exclude は両者の和集合に、組み込みの
// 既定除外（オプトアウトされていなければ）を加えたものである。exclude_defaults の
// 解決は、より具体的な repo 側の明示指定を defaults より優先する（どちらも未指定なら
// 既定で適用＝安全側）。
func MergeCopy(defaults, repo CopySpec) CopyPlan {
	var plan CopyPlan
	for _, spec := range []CopySpec{defaults, repo} {
		for _, p := range spec.Paths {
			if !slices.Contains(plan.Paths, p) {
				plan.Paths = append(plan.Paths, p)
			}
		}
		plan.Gitignored = plan.Gitignored || spec.Gitignored
	}
	if !plan.Gitignored {
		return plan // gitignored でなければ Exclude は意味を持たない。
	}

	for _, spec := range []CopySpec{defaults, repo} {
		for _, ex := range spec.Exclude {
			if !slices.Contains(plan.Exclude, ex) {
				plan.Exclude = append(plan.Exclude, ex)
			}
		}
	}

	applyBuiltins := repo.excludeDefaults()
	if repo.ExcludeDefaults == nil {
		applyBuiltins = defaults.excludeDefaults()
	}
	if applyBuiltins {
		for _, ex := range builtinExcludeDefaults {
			if !slices.Contains(plan.Exclude, ex) {
				plan.Exclude = append(plan.Exclude, ex)
			}
		}
	}
	return plan
}
