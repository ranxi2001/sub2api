package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *opsRepository) InsertNetworkSample(ctx context.Context, p service.OpsNetworkPoint) error {
	return insertNetworkSample(ctx, r.db, p)
}
func insertNetworkSample(ctx context.Context, db sqlExecutor, p service.OpsNetworkPoint) error {
	_, err := db.ExecContext(ctx, `INSERT INTO ops_network_minutes(instance_id,sampled_at,egress_mbps,tcp_retrans_percent,qdisc_drops,egress_status,tcp_status,qdisc_status,window_seconds)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(instance_id,sampled_at) DO NOTHING`, p.Instance, p.At, p.EgressMbps, p.RetransPercent, p.QdiscDrops, p.EgressStatus, p.TCPStatus, p.QdiscStatus, p.WindowSeconds)
	return err
}

func (r *opsRepository) ReadNetworkSeries(ctx context.Context, start, end time.Time) (*service.OpsNetworkSeries, error) {
	return readNetworkSeries(ctx, r.db, start, end)
}
func readNetworkSeries(ctx context.Context, db sqlExecutor, start, end time.Time) (*service.OpsNetworkSeries, error) {
	out := &service.OpsNetworkSeries{Points: []service.OpsNetworkPoint{}, BodyPoints: []service.OpsBodyBytesPoint{}, Scope: "per_process_network_namespace; qdisc=root_egress_read_only_rtnetlink_deltas; body_bytes=not_collected; client_socket_and_upload_not_collected"}
	rows, err := db.QueryContext(ctx, `SELECT date_trunc('minute',sampled_at),instance_id::text,AVG(egress_mbps),AVG(tcp_retrans_percent),SUM(qdisc_drops),
 CASE WHEN bool_and(egress_status='measured') THEN 'measured' ELSE string_agg(DISTINCT egress_status,',') END,
 CASE WHEN bool_and(tcp_status='measured') THEN 'measured' ELSE string_agg(DISTINCT tcp_status,',') END,
 string_agg(DISTINCT qdisc_status,','),AVG(window_seconds)
 FROM ops_network_minutes WHERE sampled_at >= $1 AND sampled_at < $2
 GROUP BY 1,2 ORDER BY 1,2 LIMIT 20000`, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p service.OpsNetworkPoint
		if err = rows.Scan(&p.At, &p.Instance, &p.EgressMbps, &p.RetransPercent, &p.QdiscDrops, &p.EgressStatus, &p.TCPStatus, &p.QdiscStatus, &p.WindowSeconds); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out.Points = append(out.Points, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return out, nil
}
