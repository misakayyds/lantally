package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
)

type PreviewMatch struct {
	Name            string                 `json:"name"`
	Factor          float64                `json:"factor"`
	MatchSource     accounting.MatchSource `json:"match_source"`
	MatchedRule     string                 `json:"matched_rule,omitempty"`
	MatchedRuleID   int64                  `json:"matched_rule_id,omitempty"`
	MatchedRuleName string                 `json:"matched_rule_name,omitempty"`
	MatchedType     accounting.MatchType   `json:"matched_type,omitempty"`
}

func (s *Store) ListMultiplierRules(ctx context.Context) ([]accounting.MultiplierRule, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, match_type, pattern, factor, priority, enabled, updated_at
		 FROM multiplier_rules
		 ORDER BY priority DESC, id ASC, name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []accounting.MultiplierRule{}
	for rows.Next() {
		var r accounting.MultiplierRule
		var matchType string
		var enabledInt int
		if err := rows.Scan(
			&r.ID,
			&r.Name,
			&matchType,
			&r.Pattern,
			&r.Factor,
			&r.Priority,
			&enabledInt,
			&r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.MatchType = accounting.MatchType(matchType)
		r.Enabled = enabledInt != 0
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (s *Store) CompiledMultiplierRules(ctx context.Context) (*accounting.CompiledRuleSet, error) {
	rules, err := s.ListMultiplierRules(ctx)
	if err != nil {
		return nil, err
	}
	return accounting.CompileRules(rules)
}

func (s *Store) CreateMultiplierRule(ctx context.Context, rule accounting.MultiplierRule) (accounting.MultiplierRule, error) {
	if err := accounting.ValidateRule(&rule); err != nil {
		return accounting.MultiplierRule{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	rule.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if rule.Enabled {
		enabledInt = 1
	}
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO multiplier_rules (name, match_type, pattern, factor, priority, enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rule.Name,
		string(rule.MatchType),
		rule.Pattern,
		rule.Factor,
		rule.Priority,
		enabledInt,
		rule.UpdatedAt,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return accounting.MultiplierRule{}, fmt.Errorf("%w: duplicate exact pattern %q", ErrInvalidInput, rule.Pattern)
		}
		return accounting.MultiplierRule{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return accounting.MultiplierRule{}, err
	}
	rule.ID = id
	return rule, nil
}

func (s *Store) UpdateMultiplierRule(ctx context.Context, id int64, rule accounting.MultiplierRule) (accounting.MultiplierRule, error) {
	if id <= 0 {
		return accounting.MultiplierRule{}, fmt.Errorf("%w: invalid rule id", ErrInvalidInput)
	}
	if err := accounting.ValidateRule(&rule); err != nil {
		return accounting.MultiplierRule{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	rule.ID = id
	rule.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	enabledInt := 0
	if rule.Enabled {
		enabledInt = 1
	}
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE multiplier_rules
		 SET name = ?, match_type = ?, pattern = ?, factor = ?, priority = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		rule.Name,
		string(rule.MatchType),
		rule.Pattern,
		rule.Factor,
		rule.Priority,
		enabledInt,
		rule.UpdatedAt,
		id,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return accounting.MultiplierRule{}, fmt.Errorf("%w: duplicate exact pattern %q", ErrInvalidInput, rule.Pattern)
		}
		return accounting.MultiplierRule{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return accounting.MultiplierRule{}, err
	}
	if n == 0 {
		return accounting.MultiplierRule{}, sql.ErrNoRows
	}
	return rule, nil
}

func (s *Store) DeleteMultiplierRule(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid rule id", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM multiplier_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ImportMultiplierRules(
	ctx context.Context,
	mode string,
	rules []accounting.MultiplierRule,
) ([]accounting.MultiplierRule, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "merge" {
		return nil, fmt.Errorf("%w: invalid import mode %q", ErrInvalidInput, mode)
	}

	normalized := make([]accounting.MultiplierRule, len(rules))
	exactSeen := map[string]struct{}{}
	for i := range rules {
		r := rules[i]
		if err := accounting.ValidateRule(&r); err != nil {
			return nil, fmt.Errorf("%w: rule %d: %v", ErrInvalidInput, i, err)
		}
		if mode == "replace" && r.MatchType == accounting.MatchExact {
			if _, dup := exactSeen[r.Pattern]; dup {
				return nil, fmt.Errorf("%w: rule %d: duplicate exact pattern %q", ErrInvalidInput, i, r.Pattern)
			}
			exactSeen[r.Pattern] = struct{}{}
		}
		normalized[i] = r
	}
	if _, err := accounting.CompileRules(normalized); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if mode == "replace" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM multiplier_rules`); err != nil {
			return nil, err
		}
	}

	for _, r := range normalized {
		enabledInt := 0
		if r.Enabled {
			enabledInt = 1
		}
		if mode == "merge" {
			if r.MatchType == accounting.MatchExact {
				if _, err = tx.ExecContext(
					ctx,
					`INSERT INTO multiplier_rules (name, match_type, pattern, factor, priority, enabled, updated_at)
					 VALUES (?, 'exact', ?, ?, ?, ?, ?)
					 ON CONFLICT(match_type, pattern) WHERE match_type = 'exact'
					 DO UPDATE SET name = excluded.name, factor = excluded.factor, priority = excluded.priority, enabled = excluded.enabled, updated_at = excluded.updated_at`,
					r.Name,
					r.Pattern,
					r.Factor,
					r.Priority,
					enabledInt,
					now,
				); err != nil {
					return nil, err
				}
				continue
			}

			var existingID int64
			var findErr error
			if r.MatchType == accounting.MatchAutoExtract && strings.TrimSpace(r.Pattern) == "" {
				findErr = tx.QueryRowContext(
					ctx,
					`SELECT id FROM multiplier_rules WHERE match_type = ? AND name = ? ORDER BY id ASC LIMIT 1`,
					string(r.MatchType),
					r.Name,
				).Scan(&existingID)
			} else {
				findErr = tx.QueryRowContext(
					ctx,
					`SELECT id FROM multiplier_rules WHERE match_type = ? AND pattern = ? ORDER BY id ASC LIMIT 1`,
					string(r.MatchType),
					r.Pattern,
				).Scan(&existingID)
			}
			if findErr == nil {
				if _, err = tx.ExecContext(
					ctx,
					`UPDATE multiplier_rules
					 SET name = ?, factor = ?, priority = ?, enabled = ?, updated_at = ?
					 WHERE id = ?`,
					r.Name,
					r.Factor,
					r.Priority,
					enabledInt,
					now,
					existingID,
				); err != nil {
					return nil, err
				}
				continue
			}
			if !errors.Is(findErr, sql.ErrNoRows) {
				err = findErr
				return nil, err
			}
		}

		if _, err = tx.ExecContext(
			ctx,
			`INSERT INTO multiplier_rules (name, match_type, pattern, factor, priority, enabled, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.Name,
			string(r.MatchType),
			r.Pattern,
			r.Factor,
			r.Priority,
			enabledInt,
			now,
		); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListMultiplierRules(ctx)
}

func (s *Store) PreviewMultiplierRules(
	ctx context.Context,
	draftRules *[]accounting.MultiplierRule,
	sampleNames []string,
) ([]PreviewMatch, error) {
	var compiled *accounting.CompiledRuleSet
	var err error
	if draftRules != nil {
		compiled, err = accounting.CompileRules(*draftRules)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	} else {
		compiled, err = s.CompiledMultiplierRules(ctx)
		if err != nil {
			return nil, err
		}
	}

	nameSet := map[string]struct{}{}
	catalogNames, err := s.ListProxyNodeNames(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range catalogNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" && !strings.EqualFold(trimmed, "DIRECT") {
			nameSet[trimmed] = struct{}{}
		}
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT DISTINCT outbound FROM ledger_totals WHERE class = ? AND outbound != ''`,
		accounting.ClassProxyRaw,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ob string
		if err := rows.Scan(&ob); err != nil {
			return nil, err
		}
		if trimmed := strings.TrimSpace(ob); trimmed != "" && !strings.EqualFold(trimmed, "DIRECT") {
			nameSet[trimmed] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, sample := range sampleNames {
		if trimmed := strings.TrimSpace(sample); trimmed != "" {
			nameSet[trimmed] = struct{}{}
		}
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)

	matches := make([]PreviewMatch, 0, len(names))
	for _, name := range names {
		m := compiled.Match(name)
		matches = append(matches, PreviewMatch{
			Name:            name,
			Factor:          m.Factor,
			MatchSource:     m.MatchSource,
			MatchedRule:     m.MatchedRule,
			MatchedRuleID:   m.MatchedRuleID,
			MatchedRuleName: m.MatchedRuleName,
			MatchedType:     m.MatchedType,
		})
	}
	return matches, nil
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
