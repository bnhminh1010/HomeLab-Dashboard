package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/history"
)

// Remote snapshots permit two minutes of clock skew. Rebuilding five minutes
// keeps late raw samples inside the next minute rollup with scheduler margin.
const hostMinuteRollupOverlapSeconds int64 = 5 * 60

func (s *Store) WriteHistoryBatch(ctx context.Context, batch history.Batch) error {
	if batch.Len() == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history batch: %w", err)
	}
	defer tx.Rollback()

	for _, sample := range batch.Hosts {
		if err := writeHostSample(ctx, tx, sample); err != nil {
			return err
		}
	}
	for _, sample := range batch.Containers {
		if err := writeContainerSample(ctx, tx, sample); err != nil {
			return err
		}
	}
	for _, transition := range batch.ServiceTransitions {
		if err := writeServiceTransition(ctx, tx, transition); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history batch: %w", err)
	}
	return nil
}

func writeHostSample(ctx context.Context, tx *sql.Tx, sample history.HostSample) error {
	nodeID := defaultNode(sample.NodeID)
	at, err := unixTimestamp(sample.CollectedAt)
	if err != nil {
		return fmt.Errorf("write host history: %w", err)
	}
	if err := ensureHistoryNode(ctx, tx, nodeID, at); err != nil {
		return err
	}
	memoryUsed, err := historyInteger(sample.MemoryUsedBytes)
	if err != nil {
		return fmt.Errorf("write host memory usage: %w", err)
	}
	memoryTotal, err := historyInteger(sample.MemoryTotalBytes)
	if err != nil {
		return fmt.Errorf("write host memory total: %w", err)
	}
	diskUsed, err := historyInteger(sample.DiskUsedBytes)
	if err != nil {
		return fmt.Errorf("write host disk usage: %w", err)
	}
	diskTotal, err := historyInteger(sample.DiskTotalBytes)
	if err != nil {
		return fmt.Errorf("write host disk total: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history_host_raw(
			node_id, collected_at, cpu_percent, memory_used_bytes, memory_total_bytes,
			disk_used_bytes, disk_total_bytes, network_rx_bytes_per_second,
			network_tx_bytes_per_second, load_one, temperature_celsius
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, collected_at) DO UPDATE SET
			cpu_percent = excluded.cpu_percent,
			memory_used_bytes = excluded.memory_used_bytes,
			memory_total_bytes = excluded.memory_total_bytes,
			disk_used_bytes = excluded.disk_used_bytes,
			disk_total_bytes = excluded.disk_total_bytes,
			network_rx_bytes_per_second = excluded.network_rx_bytes_per_second,
			network_tx_bytes_per_second = excluded.network_tx_bytes_per_second,
			load_one = excluded.load_one,
			temperature_celsius = excluded.temperature_celsius`,
		nodeID, at, sample.CPUPercent, memoryUsed, memoryTotal, diskUsed, diskTotal,
		sample.NetworkRXBytesPerSecond, sample.NetworkTXBytesPerSecond,
		sample.LoadOne, nullableFloat(sample.TemperatureCelsius))
	if err != nil {
		return fmt.Errorf("write host history: %w", err)
	}
	return nil
}

func writeContainerSample(ctx context.Context, tx *sql.Tx, sample history.ContainerSample) error {
	if sample.InstanceID == "" || sample.Name == "" {
		return errors.New("write container history: instance id and name are required")
	}
	nodeID := defaultNode(sample.NodeID)
	at, err := unixTimestamp(sample.CollectedAt)
	if err != nil {
		return fmt.Errorf("write container history: %w", err)
	}
	if err := ensureHistoryNode(ctx, tx, nodeID, at); err != nil {
		return err
	}
	memoryUsage, err := historyInteger(sample.MemoryUsageBytes)
	if err != nil {
		return fmt.Errorf("write container memory usage: %w", err)
	}
	memoryLimit, err := historyInteger(sample.MemoryLimitBytes)
	if err != nil {
		return fmt.Errorf("write container memory limit: %w", err)
	}
	restarts, err := historyInteger(sample.RestartCount)
	if err != nil {
		return fmt.Errorf("write container restart count: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO history_container_instances(
			node_id, instance_id, name, image, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, instance_id) DO UPDATE SET
			name = excluded.name,
			image = excluded.image,
			first_seen_at = min(first_seen_at, excluded.first_seen_at),
			last_seen_at = max(last_seen_at, excluded.last_seen_at)`,
		nodeID, sample.InstanceID, sample.Name, sample.Image, at, at); err != nil {
		return fmt.Errorf("upsert container instance: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO history_container_raw(
			node_id, instance_id, collected_at, cpu_percent, memory_usage_bytes,
			memory_limit_bytes, restart_count
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, instance_id, collected_at) DO UPDATE SET
			cpu_percent = excluded.cpu_percent,
			memory_usage_bytes = excluded.memory_usage_bytes,
			memory_limit_bytes = excluded.memory_limit_bytes,
			restart_count = excluded.restart_count`,
		nodeID, sample.InstanceID, at, sample.CPUPercent, memoryUsage,
		memoryLimit, restarts); err != nil {
		return fmt.Errorf("write container history: %w", err)
	}
	return nil
}

func writeServiceTransition(ctx context.Context, tx *sql.Tx, transition history.ServiceTransition) error {
	if transition.ServiceID == "" || !transition.State.Valid() {
		return errors.New("write service transition: service id and valid state are required")
	}
	nodeID := defaultNode(transition.NodeID)
	at, err := unixTimestamp(transition.ObservedAt)
	if err != nil {
		return fmt.Errorf("write service transition: %w", err)
	}
	if err := ensureHistoryNode(ctx, tx, nodeID, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO history_service_transitions(node_id, service_id, state, observed_at)
		VALUES (?, ?, ?, ?)`, nodeID, transition.ServiceID, string(transition.State), at); err != nil {
		return fmt.Errorf("write service transition: %w", err)
	}
	return nil
}

func ensureHistoryNode(ctx context.Context, tx *sql.Tx, nodeID string, at int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO history_nodes(id, display_name, created_at) VALUES (?, ?, ?)`,
		nodeID, nodeID, at); err != nil {
		return fmt.Errorf("ensure history node: %w", err)
	}
	return nil
}

func defaultNode(nodeID string) string {
	if nodeID == "" {
		return history.LocalNodeID
	}
	return nodeID
}

func unixTimestamp(value time.Time) (int64, error) {
	if value.IsZero() {
		return 0, errors.New("timestamp is required")
	}
	return value.UTC().Unix(), nil
}

func historyInteger(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, errors.New("value exceeds sqlite integer range")
	}
	return int64(value), nil
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (s *Store) RollupHistory(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("roll up history: timestamp is required")
	}
	nowUnix := now.UTC().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history rollup: %w", err)
	}
	defer tx.Rollback()

	jobs := []func(context.Context, *sql.Tx, int64) error{
		rollupHostMinute,
		rollupHostQuarterHour,
		rollupContainerFiveMinutes,
		rollupContainerHour,
		rollupServiceHours,
	}
	for _, job := range jobs {
		if err := job(ctx, tx, nowUnix); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history rollup: %w", err)
	}
	return nil
}

func rollupHostMinute(ctx context.Context, tx *sql.Tx, now int64) error {
	const job = "host_1m"
	start, err := rollupStart(ctx, tx, job, hostMinuteRollupOverlapSeconds)
	if err != nil {
		return err
	}
	start = (start / 60) * 60
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history_host_rollup_1m(
			node_id, bucket_at, sample_count, cpu_percent, memory_used_bytes,
			memory_total_bytes, disk_used_bytes, disk_total_bytes,
			network_rx_bytes_per_second, network_tx_bytes_per_second, load_one,
			temperature_celsius, temperature_sample_count
		)
		SELECT node_id, (collected_at / 60) * 60, count(*), avg(cpu_percent),
			avg(memory_used_bytes), avg(memory_total_bytes), avg(disk_used_bytes),
			avg(disk_total_bytes), avg(network_rx_bytes_per_second),
			avg(network_tx_bytes_per_second), avg(load_one), avg(temperature_celsius),
			count(temperature_celsius)
		FROM history_host_raw
		WHERE collected_at >= ? AND collected_at < ?
		GROUP BY node_id, (collected_at / 60) * 60
		ON CONFLICT(node_id, bucket_at) DO UPDATE SET
			sample_count = excluded.sample_count,
			cpu_percent = excluded.cpu_percent,
			memory_used_bytes = excluded.memory_used_bytes,
			memory_total_bytes = excluded.memory_total_bytes,
			disk_used_bytes = excluded.disk_used_bytes,
			disk_total_bytes = excluded.disk_total_bytes,
			network_rx_bytes_per_second = excluded.network_rx_bytes_per_second,
			network_tx_bytes_per_second = excluded.network_tx_bytes_per_second,
			load_one = excluded.load_one,
			temperature_celsius = excluded.temperature_celsius,
			temperature_sample_count = excluded.temperature_sample_count`, start, now)
	if err != nil {
		return fmt.Errorf("roll up host history to one minute: %w", err)
	}
	return setRollupCursor(ctx, tx, job, now)
}

func rollupHostQuarterHour(ctx context.Context, tx *sql.Tx, now int64) error {
	const job = "host_15m"
	start, err := rollupStart(ctx, tx, job, 15*60)
	if err != nil {
		return err
	}
	start = (start / 900) * 900
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history_host_rollup_15m(
			node_id, bucket_at, sample_count, cpu_percent, memory_used_bytes,
			memory_total_bytes, disk_used_bytes, disk_total_bytes,
			network_rx_bytes_per_second, network_tx_bytes_per_second, load_one,
			temperature_celsius, temperature_sample_count
		)
		SELECT node_id, (bucket_at / 900) * 900, sum(sample_count),
			sum(cpu_percent * sample_count) / sum(sample_count),
			sum(memory_used_bytes * sample_count) / sum(sample_count),
			sum(memory_total_bytes * sample_count) / sum(sample_count),
			sum(disk_used_bytes * sample_count) / sum(sample_count),
			sum(disk_total_bytes * sample_count) / sum(sample_count),
			sum(network_rx_bytes_per_second * sample_count) / sum(sample_count),
			sum(network_tx_bytes_per_second * sample_count) / sum(sample_count),
			sum(load_one * sample_count) / sum(sample_count),
			sum(temperature_celsius * temperature_sample_count) /
				nullif(sum(temperature_sample_count), 0),
			sum(temperature_sample_count)
		FROM history_host_rollup_1m
		WHERE bucket_at >= ? AND bucket_at < ?
		GROUP BY node_id, (bucket_at / 900) * 900
		ON CONFLICT(node_id, bucket_at) DO UPDATE SET
			sample_count = excluded.sample_count,
			cpu_percent = excluded.cpu_percent,
			memory_used_bytes = excluded.memory_used_bytes,
			memory_total_bytes = excluded.memory_total_bytes,
			disk_used_bytes = excluded.disk_used_bytes,
			disk_total_bytes = excluded.disk_total_bytes,
			network_rx_bytes_per_second = excluded.network_rx_bytes_per_second,
			network_tx_bytes_per_second = excluded.network_tx_bytes_per_second,
			load_one = excluded.load_one,
			temperature_celsius = excluded.temperature_celsius,
			temperature_sample_count = excluded.temperature_sample_count`, start, now)
	if err != nil {
		return fmt.Errorf("roll up host history to fifteen minutes: %w", err)
	}
	return setRollupCursor(ctx, tx, job, now)
}

func rollupContainerFiveMinutes(ctx context.Context, tx *sql.Tx, now int64) error {
	const job = "container_5m"
	start, err := rollupStart(ctx, tx, job, 5*60)
	if err != nil {
		return err
	}
	start = (start / 300) * 300
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history_container_rollup_5m(
			node_id, instance_id, bucket_at, sample_count, cpu_percent,
			memory_usage_bytes, memory_limit_bytes, restart_count
		)
		SELECT node_id, instance_id, (collected_at / 300) * 300, count(*),
			avg(cpu_percent), avg(memory_usage_bytes), avg(memory_limit_bytes),
			max(restart_count)
		FROM history_container_raw
		WHERE collected_at >= ? AND collected_at < ?
		GROUP BY node_id, instance_id, (collected_at / 300) * 300
		ON CONFLICT(node_id, instance_id, bucket_at) DO UPDATE SET
			sample_count = excluded.sample_count,
			cpu_percent = excluded.cpu_percent,
			memory_usage_bytes = excluded.memory_usage_bytes,
			memory_limit_bytes = excluded.memory_limit_bytes,
			restart_count = excluded.restart_count`, start, now)
	if err != nil {
		return fmt.Errorf("roll up container history to five minutes: %w", err)
	}
	return setRollupCursor(ctx, tx, job, now)
}

func rollupContainerHour(ctx context.Context, tx *sql.Tx, now int64) error {
	const job = "container_1h"
	start, err := rollupStart(ctx, tx, job, 60*60)
	if err != nil {
		return err
	}
	start = (start / 3600) * 3600
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history_container_rollup_1h(
			node_id, instance_id, bucket_at, sample_count, cpu_percent,
			memory_usage_bytes, memory_limit_bytes, restart_count
		)
		SELECT node_id, instance_id, (bucket_at / 3600) * 3600,
			sum(sample_count),
			sum(cpu_percent * sample_count) / sum(sample_count),
			sum(memory_usage_bytes * sample_count) / sum(sample_count),
			sum(memory_limit_bytes * sample_count) / sum(sample_count),
			max(restart_count)
		FROM history_container_rollup_5m
		WHERE bucket_at >= ? AND bucket_at < ?
		GROUP BY node_id, instance_id, (bucket_at / 3600) * 3600
		ON CONFLICT(node_id, instance_id, bucket_at) DO UPDATE SET
			sample_count = excluded.sample_count,
			cpu_percent = excluded.cpu_percent,
			memory_usage_bytes = excluded.memory_usage_bytes,
			memory_limit_bytes = excluded.memory_limit_bytes,
			restart_count = excluded.restart_count`, start, now)
	if err != nil {
		return fmt.Errorf("roll up container history to one hour: %w", err)
	}
	return setRollupCursor(ctx, tx, job, now)
}

func rollupStart(ctx context.Context, tx *sql.Tx, job string, overlap int64) (int64, error) {
	var cursor int64
	err := tx.QueryRowContext(ctx, `SELECT cursor_at FROM history_maintenance WHERE job = ?`, job).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read history rollup cursor %s: %w", job, err)
	}
	if cursor <= overlap {
		return 0, nil
	}
	return cursor - overlap, nil
}

func setRollupCursor(ctx context.Context, tx *sql.Tx, job string, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO history_maintenance(job, cursor_at) VALUES (?, ?)
		ON CONFLICT(job) DO UPDATE SET cursor_at = excluded.cursor_at`, job, now); err != nil {
		return fmt.Errorf("update history rollup cursor %s: %w", job, err)
	}
	return nil
}

type serviceSeries struct {
	nodeID    string
	serviceID string
}

type storedTransition struct {
	state history.ServiceState
	at    int64
}

func rollupServiceHours(ctx context.Context, tx *sql.Tx, now int64) error {
	const job = "service_1h"
	start, err := rollupStart(ctx, tx, job, 60*60)
	if err != nil {
		return err
	}
	if start == 0 {
		var first sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT min(observed_at) FROM history_service_transitions`).Scan(&first)
		if err != nil {
			return fmt.Errorf("find first service transition: %w", err)
		}
		if first.Valid {
			start = first.Int64
		}
	}
	if start == 0 || start >= now {
		return setRollupCursor(ctx, tx, job, now)
	}
	start = (start / 3600) * 3600

	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT node_id, service_id
		FROM history_service_transitions
		WHERE observed_at >= ? AND observed_at < ?`, start, now)
	if err != nil {
		return fmt.Errorf("list service history series: %w", err)
	}
	series := make([]serviceSeries, 0)
	for rows.Next() {
		var item serviceSeries
		if err := rows.Scan(&item.nodeID, &item.serviceID); err != nil {
			rows.Close()
			return fmt.Errorf("scan service history series: %w", err)
		}
		series = append(series, item)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close service history series: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list service history series: %w", err)
	}

	for _, item := range series {
		transitions, err := loadServiceTransitions(ctx, tx, item, start, now)
		if err != nil {
			return err
		}
		seriesStart := start
		if len(transitions) > 0 {
			firstBucket := (transitions[0].at / 3600) * 3600
			if firstBucket > seriesStart {
				seriesStart = firstBucket
			}
		}
		if err := storeServiceHours(ctx, tx, item, transitions, seriesStart, now); err != nil {
			return err
		}
	}
	return setRollupCursor(ctx, tx, job, now)
}

func loadServiceTransitions(ctx context.Context, tx *sql.Tx, series serviceSeries, start, now int64) ([]storedTransition, error) {
	transitions := make([]storedTransition, 0)
	var anchor storedTransition
	err := tx.QueryRowContext(ctx, `
		SELECT state, observed_at FROM history_service_transitions
		WHERE node_id = ? AND service_id = ? AND observed_at < ?
		ORDER BY observed_at DESC, id DESC LIMIT 1`, series.nodeID, series.serviceID, start).Scan(
		&anchor.state, &anchor.at,
	)
	if err == nil {
		transitions = append(transitions, anchor)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load service transition anchor: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT state, observed_at FROM history_service_transitions
		WHERE node_id = ? AND service_id = ? AND observed_at >= ? AND observed_at < ?
		ORDER BY observed_at, id`, series.nodeID, series.serviceID, start, now)
	if err != nil {
		return nil, fmt.Errorf("load service transitions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var transition storedTransition
		if err := rows.Scan(&transition.state, &transition.at); err != nil {
			return nil, fmt.Errorf("scan service transition: %w", err)
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load service transitions: %w", err)
	}
	return transitions, nil
}

func storeServiceHours(
	ctx context.Context,
	tx *sql.Tx,
	series serviceSeries,
	transitions []storedTransition,
	start int64,
	now int64,
) error {
	state := history.ServiceUnknown
	validUntil := int64(0)
	observationTTL := int64(history.ServiceObservationTTL / time.Second)
	index := 0
	for index < len(transitions) && transitions[index].at < start {
		state = transitions[index].state
		if state == history.ServiceUnknown {
			validUntil = 0
		} else {
			validUntil = transitions[index].at + observationTTL
		}
		index++
	}
	if state != history.ServiceUnknown && validUntil <= start {
		state = history.ServiceUnknown
		validUntil = 0
	}
	for bucket := start; bucket < now; bucket += 3600 {
		end := bucket + 3600
		if end > now {
			end = now
		}
		cursor := bucket
		seconds := map[history.ServiceState]int64{
			history.ServiceUp: 0, history.ServiceDown: 0,
			history.ServiceDegraded: 0, history.ServiceUnknown: 0,
		}
		transitionCount := int64(0)
		for cursor < end {
			if index < len(transitions) && transitions[index].at <= cursor {
				observed := transitions[index]
				if observed.state != state {
					transitionCount++
				}
				state = observed.state
				if state == history.ServiceUnknown {
					validUntil = 0
				} else {
					validUntil = observed.at + observationTTL
				}
				index++
				continue
			}
			if state != history.ServiceUnknown && validUntil <= cursor {
				state = history.ServiceUnknown
				validUntil = 0
				transitionCount++
				continue
			}
			next := end
			if index < len(transitions) && transitions[index].at < next {
				next = transitions[index].at
			}
			// An observation exactly at expiry keeps the state continuous, so
			// expiry only wins when it is strictly earlier.
			if state != history.ServiceUnknown && validUntil < next {
				next = validUntil
			}
			seconds[state] += next - cursor
			cursor = next
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO history_service_uptime_1h(
				node_id, service_id, bucket_at, up_seconds, down_seconds,
				degraded_seconds, unknown_seconds, transition_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(node_id, service_id, bucket_at) DO UPDATE SET
				up_seconds = excluded.up_seconds,
				down_seconds = excluded.down_seconds,
				degraded_seconds = excluded.degraded_seconds,
				unknown_seconds = excluded.unknown_seconds,
				transition_count = excluded.transition_count`,
			series.nodeID, series.serviceID, bucket, seconds[history.ServiceUp],
			seconds[history.ServiceDown], seconds[history.ServiceDegraded],
			seconds[history.ServiceUnknown], transitionCount); err != nil {
			return fmt.Errorf("store service uptime hour: %w", err)
		}
	}
	return nil
}

func (s *Store) RetainHistory(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("retain history: timestamp is required")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history retention: %w", err)
	}
	defer tx.Rollback()

	deletions := []struct {
		table  string
		column string
		cutoff int64
	}{
		{"history_host_raw", "collected_at", now.Add(-history.HostRawRetention).Unix()},
		{"history_host_rollup_1m", "bucket_at", now.Add(-history.HostMinuteRetention).Unix()},
		{"history_host_rollup_15m", "bucket_at", now.Add(-history.HostQuarterRetention).Unix()},
		{"history_container_raw", "collected_at", now.Add(-history.ContainerRawRetention).Unix()},
		{"history_container_rollup_5m", "bucket_at", now.Add(-history.Container5mRetention).Unix()},
		{"history_container_rollup_1h", "bucket_at", now.Add(-history.ContainerHourRetention).Unix()},
		{"history_service_uptime_1h", "bucket_at", now.Add(-history.ServiceRetention).Unix()},
	}
	for _, deletion := range deletions {
		query := fmt.Sprintf("DELETE FROM %s WHERE %s < ?", deletion.table, deletion.column)
		if _, err := tx.ExecContext(ctx, query, deletion.cutoff); err != nil {
			return fmt.Errorf("retain %s: %w", deletion.table, err)
		}
	}

	serviceCutoff := now.Add(-history.ServiceRetention).Unix()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM history_service_transitions AS old
		WHERE old.observed_at < ?
		AND old.id <> (
			SELECT anchor.id FROM history_service_transitions AS anchor
			WHERE anchor.node_id = old.node_id
			AND anchor.service_id = old.service_id
			AND anchor.observed_at < ?
			ORDER BY anchor.observed_at DESC, anchor.id DESC LIMIT 1
		)`, serviceCutoff, serviceCutoff); err != nil {
		return fmt.Errorf("retain service transitions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM history_container_instances AS instance
		WHERE instance.last_seen_at < ?
		AND NOT EXISTS (
			SELECT 1 FROM history_container_raw AS raw
			WHERE raw.node_id = instance.node_id AND raw.instance_id = instance.instance_id
		)
		AND NOT EXISTS (
			SELECT 1 FROM history_container_rollup_5m AS five
			WHERE five.node_id = instance.node_id AND five.instance_id = instance.instance_id
		)
		AND NOT EXISTS (
			SELECT 1 FROM history_container_rollup_1h AS hour
			WHERE hour.node_id = instance.node_id AND hour.instance_id = instance.instance_id
		)`, now.Add(-history.ContainerHourRetention).Unix()); err != nil {
		return fmt.Errorf("retain container instances: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history retention: %w", err)
	}
	return nil
}

func (s *Store) HistorySizeBytes(ctx context.Context) (int64, error) {
	var pageCount, freePages, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, fmt.Errorf("read sqlite page count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return 0, fmt.Errorf("read sqlite free page count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("read sqlite page size: %w", err)
	}
	return (pageCount - freePages) * pageSize, nil
}

func (s *Store) QueryHostHistory(
	ctx context.Context,
	query history.Query,
) ([]history.HostPoint, history.Resolution, error) {
	normalized, resolution, err := history.ResolveHost(query)
	if err != nil {
		return nil, "", err
	}
	table := map[history.Resolution]string{
		history.ResolutionRaw: "history_host_raw",
		history.Resolution1m:  "history_host_rollup_1m",
		history.Resolution15m: "history_host_rollup_15m",
	}[resolution]
	timeColumn := "bucket_at"
	countColumn := "sample_count"
	if resolution == history.ResolutionRaw {
		timeColumn = "collected_at"
		countColumn = "1"
	}
	statement := fmt.Sprintf(`
		SELECT %s, %s, cpu_percent, memory_used_bytes, memory_total_bytes,
			disk_used_bytes, disk_total_bytes, network_rx_bytes_per_second,
			network_tx_bytes_per_second, load_one, temperature_celsius
		FROM %s
		WHERE node_id = ? AND %s >= ? AND %s < ?
		ORDER BY %s`, timeColumn, countColumn, table, timeColumn, timeColumn, timeColumn)
	rows, err := s.db.QueryContext(ctx, statement, normalized.NodeID,
		normalized.From.Unix(), normalized.To.Unix())
	if err != nil {
		return nil, "", fmt.Errorf("query host history: %w", err)
	}
	defer rows.Close()
	points := make([]history.HostPoint, 0)
	for rows.Next() {
		var point history.HostPoint
		var at int64
		var temperature sql.NullFloat64
		if err := rows.Scan(&at, &point.SampleCount, &point.CPUPercent,
			&point.MemoryUsedBytes, &point.MemoryTotalBytes, &point.DiskUsedBytes,
			&point.DiskTotalBytes, &point.NetworkRXBytesPerSecond,
			&point.NetworkTXBytesPerSecond, &point.LoadOne, &temperature); err != nil {
			return nil, "", fmt.Errorf("scan host history: %w", err)
		}
		point.At = time.Unix(at, 0).UTC()
		if temperature.Valid {
			point.TemperatureCelsius = &temperature.Float64
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("query host history: %w", err)
	}
	return points, resolution, nil
}

func (s *Store) QueryContainerHistory(
	ctx context.Context,
	query history.Query,
) ([]history.ContainerPoint, history.Resolution, error) {
	normalized, resolution, err := history.ResolveContainer(query)
	if err != nil {
		return nil, "", err
	}
	table := map[history.Resolution]string{
		history.ResolutionRaw: "history_container_raw",
		history.Resolution5m:  "history_container_rollup_5m",
		history.Resolution1h:  "history_container_rollup_1h",
	}[resolution]
	timeColumn := "bucket_at"
	countColumn := "sample_count"
	if resolution == history.ResolutionRaw {
		timeColumn = "collected_at"
		countColumn = "1"
	}
	statement := fmt.Sprintf(`
		SELECT %s, %s, cpu_percent, memory_usage_bytes, memory_limit_bytes, restart_count
		FROM %s
		WHERE node_id = ? AND instance_id = ? AND %s >= ? AND %s < ?
		ORDER BY %s`, timeColumn, countColumn, table, timeColumn, timeColumn, timeColumn)
	rows, err := s.db.QueryContext(ctx, statement, normalized.NodeID, normalized.InstanceID,
		normalized.From.Unix(), normalized.To.Unix())
	if err != nil {
		return nil, "", fmt.Errorf("query container history: %w", err)
	}
	defer rows.Close()
	points := make([]history.ContainerPoint, 0)
	for rows.Next() {
		var point history.ContainerPoint
		var at int64
		if err := rows.Scan(&at, &point.SampleCount, &point.CPUPercent,
			&point.MemoryUsageBytes, &point.MemoryLimitBytes, &point.RestartCount); err != nil {
			return nil, "", fmt.Errorf("scan container history: %w", err)
		}
		point.At = time.Unix(at, 0).UTC()
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("query container history: %w", err)
	}
	return points, resolution, nil
}

func (s *Store) GetContainerInstance(
	ctx context.Context,
	nodeID string,
	instanceID string,
) (history.ContainerInstance, error) {
	nodeID = defaultNode(nodeID)
	var instance history.ContainerInstance
	var firstSeen, lastSeen int64
	err := s.db.QueryRowContext(ctx, `
		SELECT node_id, instance_id, name, image, first_seen_at, last_seen_at
		FROM history_container_instances WHERE node_id = ? AND instance_id = ?`,
		nodeID, instanceID).Scan(&instance.NodeID, &instance.InstanceID, &instance.Name,
		&instance.Image, &firstSeen, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return history.ContainerInstance{}, ErrNotFound
	}
	if err != nil {
		return history.ContainerInstance{}, fmt.Errorf("get container instance: %w", err)
	}
	instance.FirstSeenAt = time.Unix(firstSeen, 0).UTC()
	instance.LastSeenAt = time.Unix(lastSeen, 0).UTC()
	return instance, nil
}

func (s *Store) ListContainerInstances(ctx context.Context, nodeID string) ([]history.ContainerInstance, error) {
	nodeID = defaultNode(nodeID)
	cutoff := s.now().UTC().Add(-history.ContainerHourRetention).Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, instance_id, name, image, first_seen_at, last_seen_at
		FROM history_container_instances
		WHERE node_id = ? AND last_seen_at >= ?
		ORDER BY last_seen_at DESC, lower(name), instance_id
		LIMIT ?`, nodeID, cutoff, history.MaxResourceCatalogEntries)
	if err != nil {
		return nil, fmt.Errorf("list container history resources: %w", err)
	}
	defer rows.Close()
	instances := make([]history.ContainerInstance, 0)
	for rows.Next() {
		var instance history.ContainerInstance
		var firstSeen, lastSeen int64
		if err := rows.Scan(&instance.NodeID, &instance.InstanceID, &instance.Name,
			&instance.Image, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan container history resource: %w", err)
		}
		instance.FirstSeenAt = time.Unix(firstSeen, 0).UTC()
		instance.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list container history resources: %w", err)
	}
	return instances, nil
}

func (s *Store) ListServiceSeries(ctx context.Context, nodeID string) ([]history.ServiceSeries, error) {
	nodeID = defaultNode(nodeID)
	cutoff := s.now().UTC().Add(-history.ServiceRetention).Unix()
	rows, err := s.db.QueryContext(ctx, `
		WITH observations(service_id, first_seen_at, last_seen_at) AS (
			SELECT service_id, min(observed_at), max(observed_at)
			FROM history_service_transitions
			WHERE node_id = ? AND observed_at >= ? GROUP BY service_id
			UNION ALL
			SELECT service_id, min(bucket_at), max(bucket_at + 3600)
			FROM history_service_uptime_1h
			WHERE node_id = ? AND bucket_at >= ? GROUP BY service_id
		), series AS (
			SELECT service_id, min(first_seen_at) AS first_seen_at,
				max(last_seen_at) AS last_seen_at
			FROM observations GROUP BY service_id
		)
		SELECT ?, series.service_id,
			coalesce(nullif(services.name, ''), series.service_id),
			series.first_seen_at, series.last_seen_at
		FROM series LEFT JOIN services ON services.id = series.service_id
		ORDER BY series.last_seen_at DESC, lower(coalesce(nullif(services.name, ''), series.service_id)), series.service_id
		LIMIT ?`, nodeID, cutoff, nodeID, cutoff, nodeID, history.MaxResourceCatalogEntries)
	if err != nil {
		return nil, fmt.Errorf("list service history resources: %w", err)
	}
	defer rows.Close()
	series := make([]history.ServiceSeries, 0)
	for rows.Next() {
		var item history.ServiceSeries
		var firstSeen, lastSeen int64
		if err := rows.Scan(&item.NodeID, &item.ServiceID, &item.Name, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan service history resource: %w", err)
		}
		item.FirstSeenAt = time.Unix(firstSeen, 0).UTC()
		item.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		series = append(series, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list service history resources: %w", err)
	}
	return series, nil
}

func (s *Store) QueryServiceUptime(
	ctx context.Context,
	nodeID string,
	serviceID string,
	from time.Time,
	to time.Time,
) ([]history.ServiceUptimePoint, error) {
	if serviceID == "" || from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, history.ErrInvalidRange
	}
	nodeID = defaultNode(nodeID)
	from = from.UTC().Truncate(time.Hour)
	to = to.UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket_at, up_seconds, down_seconds, degraded_seconds,
			unknown_seconds, transition_count
		FROM history_service_uptime_1h
		WHERE node_id = ? AND service_id = ? AND bucket_at >= ? AND bucket_at < ?
		ORDER BY bucket_at`, nodeID, serviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query service uptime: %w", err)
	}
	defer rows.Close()
	points := make([]history.ServiceUptimePoint, 0)
	for rows.Next() {
		var point history.ServiceUptimePoint
		var at int64
		if err := rows.Scan(&at, &point.UpSeconds, &point.DownSeconds,
			&point.DegradedSeconds, &point.UnknownSeconds, &point.TransitionCount); err != nil {
			return nil, fmt.Errorf("scan service uptime: %w", err)
		}
		point.At = time.Unix(at, 0).UTC()
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query service uptime: %w", err)
	}
	return points, nil
}
