package gen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// Fin is the FinBench-shaped generator: a financial property graph of
// Account, Person, and Loan nodes wired by timestamped money-movement edges.
// It exists because the official LDBC FinBench datagen is a Spark cluster job
// (ADR-7); this generator mimics the shapes FinBench's transaction workload
// reads (spec 05 §2): a transfer network with power-law hub concentration, a
// simulation clock for temporal windows, and ownership/loan edges for the
// multi-hop anti-fraud traversals.
//
// Shape, deterministic from Config.Seed:
//
//   - Accounts accounts, Persons = Accounts/2 persons, Loans = Accounts/10
//     loans. Each label takes a packed block of integer ids (see idSpace), so
//     the three tables stay disjoint in one flat id space.
//   - Account.createTime is uniform over the simulation window, and 1% of
//     accounts are blocked. Loan.amount is uniform in [1000, 100000).
//   - TRANSFER: Days*TxPerDay edges, exactly TxPerDay per simulated day.
//     Timestamps are the simulation clock: day*86400 + uniform seconds. The
//     source is uniform; the destination is, with probability 1/2, one of the
//     first max(1, HubFrac*Accounts) hub accounts drawn by power-law rank
//     (exponent 1.5), else uniform — the hub concentration the TCR-style
//     oracles probe.
//   - WITHDRAW: max(1, TxPerDay/10) edges per day, uniform endpoints, same
//     clock.
//   - OWN: every account is owned by one uniform person. APPLY: every loan is
//     applied for by one uniform person. DEPOSIT: every loan deposits its
//     full amount into one uniform account. REPAY: every loan receives 1..4
//     repayments of 10..50% of its amount from uniform accounts.
type Fin struct{}

// Name returns "fin".
func (Fin) Name() string { return "fin" }

// Version is the algorithm version.
func (Fin) Version() string { return "1" }

// finFirstNames and finLastNames are the fixed pools person names are drawn
// from; part of the reproduction recipe.
var (
	finFirstNames = []string{"An", "Binh", "Chi", "Dung", "Giang", "Ha", "Khanh", "Lan", "Linh", "Mai", "Minh", "Nam", "Ngoc", "Phuong", "Quang", "Trang"}
	finLastNames  = []string{"Bui", "Dang", "Dinh", "Do", "Duong", "Ho", "Hoang", "Le", "Luu", "Ngo", "Nguyen", "Pham", "Phan", "Tran", "Trinh", "Vu"}
)

// Generate emits the financial graph. See Generator.Generate.
func (g Fin) Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	accounts := cfg.Accounts
	if accounts == 0 {
		accounts = 10000
	}
	if accounts < 2 {
		return nil, fmt.Errorf("fin: Accounts must be >= 2, got %d", accounts)
	}
	days := cfg.Days
	if days == 0 {
		days = 30
	}
	if days < 1 {
		return nil, fmt.Errorf("fin: Days must be >= 1, got %d", days)
	}
	txPerDay := cfg.TxPerDay
	if txPerDay == 0 {
		txPerDay = 10000
	}
	if txPerDay < 1 {
		return nil, fmt.Errorf("fin: TxPerDay must be >= 1, got %d", txPerDay)
	}
	hubFrac := cfg.HubFrac
	if hubFrac == 0 {
		hubFrac = 0.01
	}
	if hubFrac < 0 || hubFrac > 1 {
		return nil, fmt.Errorf("fin: HubFrac must be in [0,1], got %g", hubFrac)
	}
	persons := accounts / 2
	if persons < 1 {
		persons = 1
	}
	loans := accounts / 10
	if loans < 1 {
		loans = 1
	}
	// One packed id block per label, so the three tables stay disjoint in a
	// flat id space without leaving holes in it (see idSpace).
	var ids idSpace
	aid := ids.labeler(accounts)
	pid := ids.labeler(persons)
	lid := ids.labeler(loans)

	window := int64(days) * 86400
	hubs := int64(hubFrac * float64(accounts))
	if hubs < 1 {
		hubs = 1
	}

	prng := NewPRNG(cfg.Seed)
	sec := func() int64 { return prng.Int63n(86400) }
	ts := func() int64 { return prng.Int63n(window) }
	money := func(lo, hi float64) string {
		return strconv.FormatFloat(lo+prng.Float64()*(hi-lo), 'f', 2, 64)
	}

	// Node tables.
	acc, err := w.NodeFile("Account", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "createTime", Type: "INT64"},
		{Name: "isBlocked", Type: "BOOL"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < accounts; i++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		row := []string{aid(i), strconv.FormatInt(ts(), 10), strconv.FormatBool(prng.Float64() < 0.01)}
		if err := acc.Write(row); err != nil {
			return nil, err
		}
	}
	if err := acc.Close(); err != nil {
		return nil, err
	}

	per, err := w.NodeFile("Person", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "name", Type: "STRING"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < persons; i++ {
		name := finFirstNames[prng.Int63n(int64(len(finFirstNames)))] + " " +
			finLastNames[prng.Int63n(int64(len(finLastNames)))]
		if err := per.Write([]string{pid(i), name}); err != nil {
			return nil, err
		}
	}
	if err := per.Close(); err != nil {
		return nil, err
	}

	loan, err := w.NodeFile("Loan", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "amount", Type: "FLOAT64"},
	})
	if err != nil {
		return nil, err
	}
	loanAmounts := make([]string, loans)
	for i := int64(0); i < loans; i++ {
		loanAmounts[i] = money(1000, 100000)
		if err := loan.Write([]string{lid(i), loanAmounts[i]}); err != nil {
			return nil, err
		}
	}
	if err := loan.Close(); err != nil {
		return nil, err
	}

	moneyHeader := []engine.Column{
		{Name: "", Type: "START_ID"}, {Name: "", Type: "END_ID"}, {Name: "", Type: "TYPE"},
		{Name: "amount", Type: "FLOAT64"}, {Name: "ts", Type: "INT64"},
	}

	// TRANSFER: the hub-concentrated payment stream on the simulation clock.
	hubCDF := powerLawCDF(1, int(hubs), 1.5)
	transfer, err := w.RelFile("TRANSFER", "Account", "Account", moneyHeader)
	if err != nil {
		return nil, err
	}
	total := int64(days) * int64(txPerDay)
	for t := int64(0); t < total; t++ {
		if t%(1<<14) == 0 {
			if err := checkCanceled(ctx); err != nil {
				return nil, err
			}
		}
		day := t / int64(txPerDay)
		at := day*86400 + sec()
		src := prng.Int63n(accounts)
		var dst int64
		if prng.Float64() < 0.5 {
			dst = int64(drawDegree(hubCDF, 1, prng.Float64()) - 1)
		} else {
			dst = prng.Int63n(accounts)
		}
		if dst == src {
			dst = (dst + 1) % accounts
		}
		row := []string{aid(src), aid(dst), "TRANSFER", money(1, 10000), strconv.FormatInt(at, 10)}
		if err := transfer.Write(row); err != nil {
			return nil, err
		}
	}
	if err := transfer.Close(); err != nil {
		return nil, err
	}

	// WITHDRAW: a thinner uniform stream on the same clock.
	withdraw, err := w.RelFile("WITHDRAW", "Account", "Account", moneyHeader)
	if err != nil {
		return nil, err
	}
	wdPerDay := int64(txPerDay) / 10
	if wdPerDay < 1 {
		wdPerDay = 1
	}
	for t := int64(0); t < int64(days)*wdPerDay; t++ {
		day := t / wdPerDay
		at := day*86400 + sec()
		src := prng.Int63n(accounts)
		dst := prng.Int63n(accounts)
		if dst == src {
			dst = (dst + 1) % accounts
		}
		row := []string{aid(src), aid(dst), "WITHDRAW", money(1, 5000), strconv.FormatInt(at, 10)}
		if err := withdraw.Write(row); err != nil {
			return nil, err
		}
	}
	if err := withdraw.Close(); err != nil {
		return nil, err
	}

	// OWN: every account has exactly one owner.
	own, err := w.RelFile("OWN", "Person", "Account", relHeader())
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < accounts; i++ {
		if err := own.Write([]string{pid(prng.Int63n(persons)), aid(i), "OWN"}); err != nil {
			return nil, err
		}
	}
	if err := own.Close(); err != nil {
		return nil, err
	}

	// APPLY: every loan has exactly one applicant.
	apply, err := w.RelFile("APPLY", "Person", "Loan", relHeader())
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < loans; i++ {
		if err := apply.Write([]string{pid(prng.Int63n(persons)), lid(i), "APPLY"}); err != nil {
			return nil, err
		}
	}
	if err := apply.Close(); err != nil {
		return nil, err
	}

	// DEPOSIT: every loan pays out its full amount into one account.
	deposit, err := w.RelFile("DEPOSIT", "Loan", "Account", moneyHeader)
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < loans; i++ {
		row := []string{lid(i), aid(prng.Int63n(accounts)), "DEPOSIT", loanAmounts[i], strconv.FormatInt(ts(), 10)}
		if err := deposit.Write(row); err != nil {
			return nil, err
		}
	}
	if err := deposit.Close(); err != nil {
		return nil, err
	}

	// REPAY: 1..4 partial repayments per loan.
	repay, err := w.RelFile("REPAY", "Account", "Loan", moneyHeader)
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < loans; i++ {
		amount, _ := strconv.ParseFloat(loanAmounts[i], 64)
		k := 1 + prng.Int63n(4)
		for j := int64(0); j < k; j++ {
			part := strconv.FormatFloat(amount*(0.1+0.4*prng.Float64()), 'f', 2, 64)
			row := []string{aid(prng.Int63n(accounts)), lid(i), "REPAY", part, strconv.FormatInt(ts(), 10)}
			if err := repay.Write(row); err != nil {
				return nil, err
			}
		}
	}
	if err := repay.Close(); err != nil {
		return nil, err
	}

	m := &engine.Manifest{
		Name:             fmt.Sprintf("fin-a%d-d%d", accounts, days),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Scale:            fmt.Sprintf("a%d-d%d", accounts, days),
		Params: map[string]string{
			"accounts": iparam(accounts),
			"persons":  iparam(persons),
			"loans":    iparam(loans),
			"days":     iparam(days),
			"txPerDay": iparam(txPerDay),
			"hubFrac":  fparam(hubFrac),
		},
		Invariants: engine.Invariants{NodeCount: accounts + persons + loans},
	}
	return w.Finalize(m)
}
