package store

import (
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPointCodesAtomicCreditAndSingleUse(t *testing.T) {
	st := newRefundStore(t)
	uid := pointCodeUser(t, st, "redeemer")
	codes, err := st.GeneratePointCodes(1, 200, 2, 30, "batch")
	if err != nil {
		t.Fatal(err)
	}
	if codes[0].Code == codes[1].Code || len(normalizePointCode(codes[0].Code)) != 34 {
		t.Fatal("bad random code")
	}
	var stored string
	st.db.QueryRow(`SELECT code_hash FROM point_codes WHERE id=?`, codes[0].ID).Scan(&stored)
	if strings.Contains(stored, normalizePointCode(codes[0].Code)) || len(stored) != 64 {
		t.Fatal("plaintext persisted")
	}
	balance, amount, err := st.RedeemPointCode(uid, strings.ToLower(codes[0].Code))
	if err != nil || balance != 200 || amount != 200 {
		t.Fatalf("redeem %d %d %v", balance, amount, err)
	}
	if _, _, err := st.RedeemPointCode(uid, codes[0].Code); !errors.Is(err, ErrPointCodeUsed) {
		t.Fatal(err)
	}
	ledger, err := st.ListTransactions(uid, 10)
	if err != nil || len(ledger) != 1 || ledger[0].Type != "redeem" || ledger[0].RefID != codes[0].ID || ledger[0].BalanceAfter != 200 {
		t.Fatalf("ledger %+v %v", ledger, err)
	}
	rows, err := st.ListPointCodes("used", 0, 100)
	if err != nil || len(rows) != 1 || rows[0].RedeemedBy != uid || rows[0].RedeemedUsername != "redeemer" {
		t.Fatalf("audit %+v %v", rows, err)
	}
}

func TestPointCodeConcurrentRedemptionCreditsExactlyOnce(t *testing.T) {
	st := newRefundStore(t)
	codes, err := st.GeneratePointCodes(1, 25, 1, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	users := []int64{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		users = append(users, pointCodeUser(t, st, name))
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for _, uid := range users {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			_, _, err := st.RedeemPointCode(uid, codes[0].Code)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrPointCodeUsed) {
				t.Errorf("unexpected %v", err)
			}
		}(uid)
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("success=%d", success.Load())
	}
	var total, ledger int64
	st.db.QueryRow(`SELECT SUM(points) FROM users`).Scan(&total)
	st.db.QueryRow(`SELECT COUNT(*) FROM point_transactions WHERE type='redeem'`).Scan(&ledger)
	if total != 25 || ledger != 1 {
		t.Fatalf("points=%d ledger=%d", total, ledger)
	}
}

func TestPointCodeRollbackOnLedgerFailureAndOverflow(t *testing.T) {
	st := newRefundStore(t)
	uid := pointCodeUser(t, st, "rollback")
	codes, _ := st.GeneratePointCodes(1, 10, 1, 30, "")
	st.db.Exec(`CREATE TRIGGER reject_redeem BEFORE INSERT ON point_transactions BEGIN SELECT RAISE(ABORT, 'ledger failure'); END`)
	if _, _, err := st.RedeemPointCode(uid, codes[0].Code); err == nil {
		t.Fatal("expected ledger failure")
	}
	var used, balance int64
	st.db.QueryRow(`SELECT redeemed_at FROM point_codes`).Scan(&used)
	st.db.QueryRow(`SELECT points FROM users WHERE id=?`, uid).Scan(&balance)
	if used != 0 || balance != 0 {
		t.Fatal("partial redemption committed")
	}
	st.db.Exec(`DROP TRIGGER reject_redeem`)
	st.db.Exec(`UPDATE users SET points=? WHERE id=?`, int64(math.MaxInt64)-5, uid)
	if _, _, err := st.RedeemPointCode(uid, codes[0].Code); !errors.Is(err, ErrPointBalanceOverflow) {
		t.Fatal(err)
	}
	st.db.QueryRow(`SELECT redeemed_at FROM point_codes`).Scan(&used)
	if used != 0 {
		t.Fatal("overflow consumed code")
	}
}

func TestPointCodeStatesAndPersistentLockout(t *testing.T) {
	st := newRefundStore(t)
	uid := pointCodeUser(t, st, "locked")
	codes, _ := st.GeneratePointCodes(1, 10, 3, 30, "")
	st.DisablePointCodes([]int64{codes[0].ID})
	if _, _, err := st.RedeemPointCode(uid, codes[0].Code); !errors.Is(err, ErrPointCodeDisabled) {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE point_codes SET expires_at=? WHERE id=?`, time.Now().Unix()-1, codes[1].ID)
	if _, _, err := st.RedeemPointCode(uid, codes[1].Code); !errors.Is(err, ErrPointCodeExpired) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := st.RedeemPointCode(uid, "missing"); !errors.Is(err, ErrPointCodeMissing) {
			t.Fatal(err)
		}
	}
	if _, _, err := st.RedeemPointCode(uid, "missing"); !errors.Is(err, ErrPointCodeLocked) {
		t.Fatal(err)
	}
	if _, _, err := st.RedeemPointCode(uid, codes[2].Code); !errors.Is(err, ErrPointCodeLocked) {
		t.Fatal("lock did not block valid code")
	}
	var locked int64
	st.db.QueryRow(`SELECT locked_until FROM point_code_attempts WHERE user_id=?`, uid).Scan(&locked)
	if locked <= time.Now().Unix() {
		t.Fatal("lock not persisted")
	}
	st.db.Exec(`UPDATE point_code_attempts SET locked_until=? WHERE user_id=?`, time.Now().Unix()-1, uid)
	if balance, _, err := st.RedeemPointCode(uid, codes[2].Code); err != nil || balance != 10 {
		t.Fatalf("unlock: %d %v", balance, err)
	}
	var attempts int
	st.db.QueryRow(`SELECT COUNT(*) FROM point_code_attempts WHERE user_id=?`, uid).Scan(&attempts)
	if attempts != 0 {
		t.Fatal("success did not reset consecutive failures")
	}
	for _, status := range []string{"used", "disabled", "expired"} {
		rows, err := st.ListPointCodes(status, 0, 100)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s=%+v %v", status, rows, err)
		}
	}
}

func TestPointCodeInputAndKeysetPagination(t *testing.T) {
	st := newRefundStore(t)
	for _, args := range [][3]int64{{0, 1, 30}, {1, 0, 30}, {1, 101, 30}, {1, 1, 0}, {1, 1, 3651}, {1_000_000_001, 1, 30}} {
		if _, err := st.GeneratePointCodes(1, args[0], int(args[1]), int(args[2]), ""); err == nil {
			t.Fatalf("bad input accepted: %v", args)
		}
	}
	st.GeneratePointCodes(1, 1, 5, 30, "")
	first, _ := st.ListPointCodes("unused", 0, 2)
	second, _ := st.ListPointCodes("unused", first[1].ID, 2)
	if len(first) != 2 || len(second) != 2 || second[0].ID >= first[1].ID {
		t.Fatal("broken keyset pagination")
	}
	if _, err := st.ListPointCodes("injected", 0, 100); err == nil {
		t.Fatal("invalid filter accepted")
	}
}

func pointCodeUser(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	id, err := st.CreateUser(NewUser{Username: name, PasswordHash: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
