package router

import (
	"sync"
	"time"
)

const DefaultIntentClassifierDailyBudgetUSD = 50.0

type IntentBudgetGuard struct {
	mu        sync.Mutex
	limitUSD  float64
	spentUSD  float64
	windowDay string
	now       func() time.Time
}

func NewIntentBudgetGuard(limitUSD float64) *IntentBudgetGuard {
	if limitUSD <= 0 {
		limitUSD = DefaultIntentClassifierDailyBudgetUSD
	}
	return &IntentBudgetGuard{
		limitUSD: limitUSD,
		now:      time.Now,
	}
}

func (g *IntentBudgetGuard) SetNowForTest(now func() time.Time) {
	if g == nil || now == nil {
		return
	}
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}

func (g *IntentBudgetGuard) Allow(estimatedCostUSD float64) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetWindowLocked()
	if estimatedCostUSD < 0 {
		estimatedCostUSD = 0
	}
	return g.spentUSD+estimatedCostUSD <= g.limitUSD
}

func (g *IntentBudgetGuard) Record(costUSD float64) {
	if g == nil || costUSD <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetWindowLocked()
	g.spentUSD += costUSD
}

func (g *IntentBudgetGuard) SpentUSD() float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetWindowLocked()
	return g.spentUSD
}

func (g *IntentBudgetGuard) resetWindowLocked() {
	day := g.now().UTC().Format("2006-01-02")
	if g.windowDay == day {
		return
	}
	g.windowDay = day
	g.spentUSD = 0
}
