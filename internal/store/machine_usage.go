package store

import (
	"context"
	"database/sql"
)

// MachineUsageReport is retained sing-box user attribution, not NIC/provider
// traffic, and deliberately has no package axis: the source never recorded it.
type MachineUsageReport struct {
	CoverageStart int64                 `json:"coverage_start"`
	CoverageEnd   int64                 `json:"coverage_end"`
	Up            int64                 `json:"up"`
	Down          int64                 `json:"down"`
	UserCount     int                   `json:"user_count"`
	Users         []ServerTrafficSource `json:"users"`
	Days          []UsageDay            `json:"days"`
}

func (s *Store) MachineUsage(serverID, from, to int64, page int) (MachineUsageReport, error) {
	out := MachineUsageReport{Users: []ServerTrafficSource{}, Days: []UsageDay{}}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT COALESCE(MIN(ts),0),COALESCE(MAX(ts),0),COALESCE(SUM(up),0),COALESCE(SUM(down),0),COUNT(DISTINCT user_id) FROM server_user_traffic_samples WHERE server_id=? AND ts>=? AND ts<?`, serverID, from, to).Scan(&out.CoverageStart, &out.CoverageEnd, &out.Up, &out.Down, &out.UserCount)
	if err != nil {
		return out, err
	}
	if page < 1 {
		page = 1
	}
	rows, err := tx.Query(`SELECT t.user_id,COALESCE(NULLIF(u.username,''),'已删除用户 #' || t.user_id),SUM(t.up),SUM(t.down),SUM(t.up+t.down) FROM server_user_traffic_samples t LEFT JOIN users u ON u.id=t.user_id WHERE t.server_id=? AND t.ts>=? AND t.ts<? GROUP BY t.user_id ORDER BY SUM(t.up+t.down) DESC,t.user_id LIMIT 50 OFFSET ?`, serverID, from, to, (page-1)*50)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var row ServerTrafficSource
		if err = rows.Scan(&row.UserID, &row.Username, &row.Up, &row.Down, &row.Total); err != nil {
			rows.Close()
			return out, err
		}
		out.Users = append(out.Users, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(`SELECT strftime('%Y-%m-%d',ts,'unixepoch','localtime'),SUM(up),SUM(down) FROM server_user_traffic_samples WHERE server_id=? AND ts>=? AND ts<? GROUP BY 1 ORDER BY 1`, serverID, from, to)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d UsageDay
		if err = rows.Scan(&d.Date, &d.Up, &d.Down); err != nil {
			return out, err
		}
		out.Days = append(out.Days, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
