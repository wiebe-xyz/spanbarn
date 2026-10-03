// Package slo holds the error budget arithmetic for service level objectives.
// It is a leaf package and imports nothing else from the module.
package slo

// BudgetRemaining returns the share of the error budget still unspent. 1 means
// untouched and a value below 0 means overspent. With no traffic the budget is
// untouched. A target of 1 or more leaves no budget, so one bad event reports
// -1 and none reports 1.
func BudgetRemaining(target float64, good, total int64) float64 {
	if total <= 0 {
		return 1
	}
	allowed := (1 - target) * float64(total)
	bad := float64(total - good)
	if allowed <= 0 {
		if bad <= 0 {
			return 1
		}
		return -1
	}
	return 1 - bad/allowed
}

// BurnRate returns how fast the budget is spent. 1 means spending exactly on
// pace, 2 means twice as fast. With no traffic the burn is 0.
func BurnRate(target float64, good, total int64) float64 {
	if total <= 0 {
		return 0
	}
	budget := 1 - target
	if budget <= 0 {
		return 0
	}
	bad := float64(total - good)
	return bad / float64(total) / budget
}
