package ledger

import (
	"database/sql"
	"errors"

	"github.com/jmvaldez/bosun/internal/statusline"
)

// A fork's transcript starts with a verbatim copy of its parent's lines. The
// session that first wrote a request owns it and the copy is ignored, so a
// file that repeats rows owned by another session is a fork of that session
// (ingest records it in forked_from). orderForIngest puts the parent first,
// which makes it the first writer.

// inheritedUSD is how much of the fork's gross total belongs to the parent.
// In order of trust:
//  1. the first total the status line showed for the fork, taken before the
//     fork spent anything of its own;
//  2. the parent's TotalUSD plus BankedUSD, when the parent did nothing after the fork;
//  3. the priced parent rows up to the fork point (an estimate).
//
// The result is clamped to [0, gross], gross being the fork's TotalUSD plus
// BankedUSD (D-20). The subtraction itself happens only in OwnUSD.
func inheritedUSD(tx *sql.Tx, id, parent string, snap *statusline.Snapshot, gross float64) (float64, error) {
	var firstOwn sql.NullInt64
	if err := tx.QueryRow("SELECT MIN(ts) FROM requests WHERE session_id = ?", id).Scan(&firstOwn); err != nil {
		return 0, err
	}
	clamp := func(v float64) float64 {
		if v > gross {
			return gross
		}
		if v < 0 {
			return 0
		}
		return v
	}
	if snap != nil && snap.FirstUSD > 0 {
		first := parseMS(snap.FirstTS)
		if first != 0 && (!firstOwn.Valid || first <= firstOwn.Int64) {
			return clamp(snap.FirstUSD), nil
		}
	}
	var pTotal, pBanked float64
	var pLast int64
	if err := tx.QueryRow("SELECT total_usd, banked_usd, last_activity_at FROM sessions WHERE session_id = ?", parent).
		Scan(&pTotal, &pBanked, &pLast); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if !firstOwn.Valid || pLast <= firstOwn.Int64 {
		return clamp(pTotal + pBanked), nil
	}
	rows, err := loadSessionRequests(tx, parent)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, r := range rows {
		if r.TS.UnixMilli() <= firstOwn.Int64 {
			sum += PriceRequest(r).USD
		}
	}
	return clamp(sum), nil
}
